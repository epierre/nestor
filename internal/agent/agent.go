// Package agent turns a user's message into a reply.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/tools"
)

const (
	noticeModelFailure = "I couldn't reach the model, try again shortly."
	noticeLoopLimit    = "I couldn't finish that request, try asking in a different way."

	commandNew            = "/new"
	noticeNewConversation = "Started a new conversation."

	toolsUnavailable = "error: no tools are available"
)

// ToolRegistry looks up tools and lists them for the model.
type ToolRegistry interface {
	ToolsFor(ctx context.Context, user identity.UserID) []model.Tool
	Lookup(name string) (tools.Tool, error)
	Validate(name string, args json.RawMessage) error
}

// Approvals holds critical tool calls until their user decides on them. approval.Gate implements it.
type Approvals interface {
	Propose(ctx context.Context, user identity.UserID, tool tools.Tool, args json.RawMessage) (store.PendingAction, error)
}

// CardSender shows a pending action to its user. An error leaves the action pending: its card is
// sent again later.
type CardSender interface {
	SendApprovalCard(ctx context.Context, action store.PendingAction) error
}

// Options configures an Agent.
type Options struct {
	Model         string // proxy alias
	SystemPrompt  string
	HistoryLimit  int // messages of the conversation sent to the model
	MaxIterations int // model calls allowed for one message
	Tools         ToolRegistry
	// Approvals takes critical tool calls. Without it, critical calls are refused.
	Approvals Approvals
	// Cards shows each proposed action to its user. Without it, proposed actions have no card.
	Cards CardSender
	// Notices delivers outcome-unknown notices that were not delivered when first due. Without it,
	// they are not sent again.
	Notices approval.Notifier
	// Location adds the current local date, time and IANA zone name to the system prompt on each
	// turn when set. Without it, SystemPrompt is sent unchanged.
	Location *time.Location
	// Now returns the current time for prompt timestamps and card/proposal checks. Without it,
	// time.Now is used.
	Now func() time.Time
}

// Agent runs the conversation loop for one message at a time.
type Agent struct {
	client model.ModelClient
	store  store.Store
	opts   Options
}

// New returns an Agent using client and st.
func New(client model.ModelClient, st store.Store, opts Options) *Agent {
	if reg, ok := opts.Tools.(*tools.Registry); ok {
		opts.Tools = reg.AgentView()
	}
	return &Agent{client: client, store: st, opts: opts}
}

// HandleMessage stores text in the active conversation of the user carried by ctx, and returns and
// stores the model's reply. A failed or runaway model call yields a notice for the user, which is
// not stored so that the model never reads it as its own words. An error means the message could
// not be handled at all, such as when the store is down. The /new command closes the user's
// conversation instead.
func (a *Agent) HandleMessage(ctx context.Context, text string) (string, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return "", fmt.Errorf("handle message: %w", err)
	}
	if strings.TrimSpace(text) == commandNew {
		if err := a.store.CloseConversation(ctx, user); err != nil {
			return "", fmt.Errorf("close conversation: %w", err)
		}
		return noticeNewConversation, nil
	}
	a.resendCards(ctx, user)
	ctx = approval.WithTurn(ctx)
	conversation, err := a.store.ActiveConversation(ctx, user)
	if err != nil {
		return "", fmt.Errorf("load conversation: %w", err)
	}
	if _, err := a.store.AppendMessage(ctx, user, conversation.ID, model.RoleUser, text); err != nil {
		return "", fmt.Errorf("store user message: %w", err)
	}
	history, err := a.store.RecentMessages(ctx, user, conversation.ID, a.opts.HistoryLimit)
	if err != nil {
		return "", fmt.Errorf("load history: %w", err)
	}
	proposed, err := a.store.ProposedActions(ctx, user, a.now())
	if err != nil {
		return "", fmt.Errorf("load proposed actions: %w", err)
	}

	messages := a.prompt(history, conversation.StartedAt, proposed)
	var modelTools []model.Tool
	if a.opts.Tools != nil {
		modelTools = a.opts.Tools.ToolsFor(ctx, user)
	}

	for range a.opts.MaxIterations {
		resp, callErr := a.client.Complete(ctx, model.CompletionRequest{
			Model:    a.opts.Model,
			Messages: messages,
			Tools:    modelTools,
		})
		modelCallID, err := a.recordCalls(ctx, user, conversation.ID, resp)
		if err != nil {
			return "", err
		}
		if callErr != nil {
			slog.ErrorContext(ctx, "model call failed", "error", callErr)
			return noticeModelFailure, nil
		}
		if len(resp.ToolCalls) == 0 {
			if _, err := a.store.AppendMessage(ctx, user, conversation.ID, model.RoleAssistant, resp.Content); err != nil {
				return "", fmt.Errorf("store reply: %w", err)
			}
			return resp.Content, nil
		}
		if a.opts.Tools == nil {
			messages = appendToolRefusals(messages, resp)
		} else {
			messages, err = a.dispatchToolCalls(ctx, user, conversation.ID, modelCallID, messages, resp)
			if err != nil {
				return "", err
			}
		}
	}
	slog.WarnContext(ctx, "model call limit reached", "max_iterations", a.opts.MaxIterations)
	return noticeLoopLimit, nil
}

// recordCalls writes one call log row per attempt, so a retried call shows as two calls, and
// returns the ID of the last recorded call. A call that cannot be recorded is an error: nothing
// reaches the user that the log does not show.
func (a *Agent) recordCalls(ctx context.Context, user identity.UserID, conversationID int64, resp model.CompletionResponse) (*int64, error) {
	var lastID *int64
	for _, attempt := range resp.Attempts {
		call := store.ModelCall{
			ConversationID: conversationID,
			Model:          a.opts.Model,
			StartedAt:      attempt.StartedAt,
			Latency:        attempt.Latency,
			Status:         store.CallStatusOK,
			RequestJSON:    attempt.Request,
		}
		var err error
		if call.ResponseJSON, err = responseJSON(attempt.Response); err != nil {
			return nil, fmt.Errorf("encode response: %w", err)
		}
		if attempt.Err != nil {
			call.Status = store.CallStatusError
			call.Error = attempt.Err.Error()
		} else {
			call.PromptTokens = resp.Usage.PromptTokens
			call.CompletionTokens = resp.Usage.CompletionTokens
			call.CostUSD = resp.Usage.CostUSD
		}
		id, err := a.store.RecordModelCall(ctx, user, call)
		if err != nil {
			return nil, fmt.Errorf("record model call: %w", err)
		}
		lastID = &id
		slog.InfoContext(ctx, "model call finished",
			"model", call.Model, "status", call.Status, "latency_ms", call.Latency.Milliseconds())
	}
	return lastID, nil
}

// responseJSON keeps a body as it is, wrapping one that is not JSON, such as a proxy's plain text
// error, in a JSON string.
func responseJSON(body []byte) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, nil
	}
	if json.Valid(body) {
		return body, nil
	}
	return json.Marshal(string(body))
}

// appendToolRefusals answers each tool call with an error result, because no tools exist yet.
func appendToolRefusals(messages []model.Message, resp model.CompletionResponse) []model.Message {
	messages = append(messages, model.Message{
		Role:      model.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
	})
	for _, call := range resp.ToolCalls {
		messages = append(messages, model.Message{
			Role:       model.RoleTool,
			ToolCallID: call.ID,
			Content:    toolsUnavailable,
		})
	}
	return messages
}

func (a *Agent) dispatchToolCalls(ctx context.Context, user identity.UserID, conversationID int64, modelCallID *int64, messages []model.Message, resp model.CompletionResponse) ([]model.Message, error) {
	messages = append(messages, model.Message{
		Role:      model.RoleAssistant,
		ToolCalls: resp.ToolCalls,
	})
	for _, call := range resp.ToolCalls {
		start := time.Now()
		tool, err := a.opts.Tools.Lookup(call.Name)
		if err == nil && !tools.Enabled(ctx, tool, user) {
			// Offered to other users only. Treated as unknown so the model learns nothing more.
			err = tools.ErrNotFound
		}
		var (
			content    string
			status     string
			risk       tools.Risk
			resultJSON json.RawMessage
			errText    string
			proposed   *store.PendingAction
		)
		if err != nil {
			status = store.ToolCallStatusRejected
			errText = err.Error()
			content = tools.MarkData(fmt.Sprintf("error: unknown tool %q", call.Name))
		} else {
			risk = tool.Risk()
			if valErr := a.opts.Tools.Validate(call.Name, call.Arguments); valErr != nil {
				status = store.ToolCallStatusRejected
				errText = valErr.Error()
				content = tools.MarkData(fmt.Sprintf("error: invalid arguments: %v", valErr))
			} else if tool.Risk() == tools.RiskCritical && a.opts.Approvals == nil {
				status = store.ToolCallStatusRejected
				errText = "critical tool requires approval gate"
				content = tools.MarkData(fmt.Sprintf("error: critical tool %q requires approval gate", call.Name))
			} else if tool.Risk() == tools.RiskCritical {
				// The risk comes from the tool's definition, never from the arguments.
				action, proposeErr := a.opts.Approvals.Propose(ctx, user, tool, call.Arguments)
				if proposeErr != nil {
					return nil, fmt.Errorf("propose action: %w", proposeErr)
				}
				proposed = &action
				status = store.ToolCallStatusProposed
				pending := "pending approval " + action.ID
				resultJSON, _ = json.Marshal(pending)
				content = tools.MarkData(pending)
			} else {
				res, execErr := tool.Execute(ctx, call.Arguments)
				if execErr != nil {
					status = store.ToolCallStatusError
					errText = execErr.Error()
					content = tools.MarkData(fmt.Sprintf("error: %v", execErr))
				} else {
					status = store.ToolCallStatusExecuted
					if res.Truncated {
						status = store.ToolCallStatusTruncated
					}
					resBytes, _ := json.Marshal(res.Content)
					resultJSON = resBytes
					content = tools.MarkData(res.Content)
				}
			}
		}
		latency := time.Since(start)
		args := call.Arguments
		if len(bytes.TrimSpace(args)) == 0 {
			args = json.RawMessage("{}")
		}
		toolCallID, recordErr := a.store.RecordToolCall(ctx, user, store.ToolCall{
			ConversationID: conversationID,
			ModelCallID:    modelCallID,
			ToolName:       call.Name,
			ArgsJSON:       args,
			Risk:           risk,
			Status:         status,
			ResultJSON:     resultJSON,
			Error:          errText,
			StartedAt:      start,
			Latency:        latency,
		})
		if recordErr != nil {
			return nil, fmt.Errorf("record tool call: %w", recordErr)
		}
		if proposed != nil {
			if err := a.store.LinkActionToolCall(ctx, user, proposed.ID, toolCallID); err != nil {
				return nil, fmt.Errorf("link action tool call: %w", err)
			}
			proposed.ToolCallID = &toolCallID
			a.showCard(ctx, user, *proposed)
		}
		slog.InfoContext(ctx, "tool call finished",
			"tool", call.Name, "risk", risk, "status", status, "latency_ms", latency.Milliseconds())
		messages = append(messages, model.Message{
			Role:       model.RoleTool,
			ToolCallID: call.ID,
			Content:    content,
		})
	}
	return messages, nil
}

// showCard sends the action's card and records the delivery and its chat. A failure is logged and
// does not fail the message: the card is sent again on the user's next message.
func (a *Agent) showCard(ctx context.Context, user identity.UserID, action store.PendingAction) {
	if a.opts.Cards == nil {
		return
	}
	if err := a.opts.Cards.SendApprovalCard(ctx, action); err != nil {
		slog.WarnContext(ctx, "approval card not sent", "action_id", action.ID, "error", err)
		return
	}
	chatID, _ := channel.ChatFrom(ctx)
	if err := a.store.MarkCardSent(ctx, user, action.ID, chatID, a.now()); err != nil {
		slog.WarnContext(ctx, "approval card delivery not recorded", "action_id", action.ID, "error", err)
	}
}

// resendCards shows again the cards of the user's pending actions that were never delivered, and
// sends again the outcome notices that were never delivered.
func (a *Agent) resendCards(ctx context.Context, user identity.UserID) {
	a.resendOutcomeNotices(ctx, user)
	if a.opts.Cards == nil {
		return
	}
	unsent, err := a.store.UnsentCards(ctx, user, a.now())
	if err != nil {
		slog.WarnContext(ctx, "unsent approval cards not listed", "error", err)
		return
	}
	for _, action := range unsent {
		a.showCard(ctx, user, action)
	}
}

// resendOutcomeNotices sends the user's undelivered outcome-unknown notices, to the chat each card
// went to, or to the incoming message's chat when no card chat was recorded.
func (a *Agent) resendOutcomeNotices(ctx context.Context, user identity.UserID) {
	if a.opts.Notices == nil {
		return
	}
	waiting, err := a.store.UnnotifiedOutcomes(ctx, user)
	if err != nil {
		slog.WarnContext(ctx, "unnotified outcomes not listed", "error", err)
		return
	}
	chatID, hasChat := channel.ChatFrom(ctx)
	for _, action := range waiting {
		if action.NotifyChatID == 0 && hasChat {
			action.NotifyChatID = chatID
		}
		approval.SendOutcomeNotice(ctx, a.store, a.opts.Notices, action)
	}
}

func (a *Agent) now() time.Time {
	if a.opts.Now != nil {
		return a.opts.Now()
	}
	return time.Now()
}

func (a *Agent) systemPrompt() string {
	if a.opts.Location == nil {
		return a.opts.SystemPrompt
	}
	local := a.now().In(a.opts.Location)
	line := fmt.Sprintf("Current time: %s (%s).", local.Format("Monday, 2006-01-02 15:04 MST"), a.opts.Location.String())
	if a.opts.SystemPrompt == "" {
		return line
	}
	return a.opts.SystemPrompt + "\n" + line
}

func (a *Agent) prompt(history []store.Message, convStarted time.Time, proposed []store.PendingAction) []model.Message {
	var active []store.PendingAction
	convFloor := convStarted.Truncate(time.Microsecond)
	for _, p := range proposed {
		if convStarted.IsZero() || !p.CreatedAt.Before(convFloor) {
			active = append(active, p)
		}
	}

	// Reconstruct open proposals right after the prior user turn that produced them so the model can
	// edit them without any untrusted arguments or previews entering system or user messages.
	byIndex := make(map[int][]store.PendingAction)
	if len(active) > 0 && len(history) > 1 {
		firstUser := -1
		for i := 0; i < len(history)-1; i++ {
			if history[i].Role == model.RoleUser {
				firstUser = i
				break
			}
		}
		if firstUser >= 0 {
			for _, p := range active {
				target := firstUser
				for i := firstUser; i < len(history)-1; i++ {
					if history[i].Role == model.RoleUser && !history[i].CreatedAt.Truncate(time.Microsecond).After(p.CreatedAt) {
						target = i
					}
				}
				byIndex[target] = append(byIndex[target], p)
			}
		}
	}

	messages := make([]model.Message, 0, len(history)+1+2*len(active))
	messages = append(messages, model.Message{Role: model.RoleSystem, Content: a.systemPrompt()})
	for i, m := range history {
		messages = append(messages, model.Message{Role: m.Role, Content: m.Content})
		if group := byIndex[i]; len(group) > 0 {
			calls := make([]model.ToolCall, 0, len(group))
			for _, p := range group {
				calls = append(calls, model.ToolCall{
					ID:        "pending_" + p.ID,
					Name:      p.ToolName,
					Arguments: p.ArgsJSON,
				})
			}
			messages = append(messages, model.Message{
				Role:      model.RoleAssistant,
				ToolCalls: calls,
			})
			for _, p := range group {
				messages = append(messages, model.Message{
					Role:       model.RoleTool,
					ToolCallID: "pending_" + p.ID,
					Content:    tools.MarkData("pending approval " + p.ID),
				})
			}
		}
	}
	return messages
}
