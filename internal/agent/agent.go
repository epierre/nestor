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
	Tools() []model.Tool
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

	messages := a.prompt(history)
	var modelTools []model.Tool
	if a.opts.Tools != nil {
		modelTools = a.opts.Tools.Tools()
	}

	for range a.opts.MaxIterations {
		resp, callErr := a.client.Complete(ctx, model.CompletionRequest{
			Model:    a.opts.Model,
			Messages: messages,
			Tools:    modelTools,
		})
		if err := a.recordCalls(ctx, user, conversation.ID, resp); err != nil {
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
			var err error
			messages, err = a.dispatchToolCalls(ctx, user, conversation.ID, messages, resp)
			if err != nil {
				return "", err
			}
		}
	}
	slog.WarnContext(ctx, "model call limit reached", "max_iterations", a.opts.MaxIterations)
	return noticeLoopLimit, nil
}

// recordCalls writes one call log row per attempt, so a retried call shows as two calls. A call
// that cannot be recorded is an error: nothing reaches the user that the log does not show.
func (a *Agent) recordCalls(ctx context.Context, user identity.UserID, conversationID int64, resp model.CompletionResponse) error {
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
			return fmt.Errorf("encode response: %w", err)
		}
		if attempt.Err != nil {
			call.Status = store.CallStatusError
			call.Error = attempt.Err.Error()
		} else {
			call.PromptTokens = resp.Usage.PromptTokens
			call.CompletionTokens = resp.Usage.CompletionTokens
			call.CostUSD = resp.Usage.CostUSD
		}
		if err := a.store.RecordModelCall(ctx, user, call); err != nil {
			return fmt.Errorf("record model call: %w", err)
		}
		slog.InfoContext(ctx, "model call finished",
			"model", call.Model, "status", call.Status, "latency_ms", call.Latency.Milliseconds())
	}
	return nil
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

func (a *Agent) dispatchToolCalls(ctx context.Context, user identity.UserID, conversationID int64, messages []model.Message, resp model.CompletionResponse) ([]model.Message, error) {
	messages = append(messages, model.Message{
		Role:      model.RoleAssistant,
		ToolCalls: resp.ToolCalls,
	})
	for _, call := range resp.ToolCalls {
		start := time.Now()
		tool, err := a.opts.Tools.Lookup(call.Name)
		var (
			content    string
			status     string
			risk       tools.Risk
			resultJSON json.RawMessage
			errText    string
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
				a.showCard(ctx, action)
				status = store.ToolCallStatusProposed
				content = "pending approval " + action.ID
				resultJSON, _ = json.Marshal(content)
			} else {
				res, execErr := tool.Execute(ctx, call.Arguments)
				if execErr != nil {
					status = store.ToolCallStatusError
					errText = execErr.Error()
					content = tools.MarkData(fmt.Sprintf("error: %v", execErr))
				} else {
					status = store.ToolCallStatusExecuted
					resBytes, _ := json.Marshal(res.Content)
					resultJSON = resBytes
					content = tools.MarkData(res.Content)
				}
			}
		}
		args := call.Arguments
		if len(bytes.TrimSpace(args)) == 0 {
			args = json.RawMessage("{}")
		}
		recordErr := a.store.RecordToolCall(ctx, user, store.ToolCall{
			ConversationID: conversationID,
			ToolName:       call.Name,
			ArgsJSON:       args,
			Risk:           risk,
			Status:         status,
			ResultJSON:     resultJSON,
			Error:          errText,
			StartedAt:      start,
			Latency:        time.Since(start),
		})
		if recordErr != nil {
			return nil, fmt.Errorf("record tool call: %w", recordErr)
		}
		messages = append(messages, model.Message{
			Role:       model.RoleTool,
			ToolCallID: call.ID,
			Content:    content,
		})
	}
	return messages, nil
}

// showCard sends the action's card. A failure is logged and does not fail the message.
func (a *Agent) showCard(ctx context.Context, action store.PendingAction) {
	if a.opts.Cards == nil {
		return
	}
	if err := a.opts.Cards.SendApprovalCard(ctx, action); err != nil {
		slog.WarnContext(ctx, "approval card not sent", "action_id", action.ID, "error", err)
	}
}

func (a *Agent) prompt(history []store.Message) []model.Message {
	messages := make([]model.Message, 0, len(history)+1)
	messages = append(messages, model.Message{Role: model.RoleSystem, Content: a.opts.SystemPrompt})
	for _, m := range history {
		messages = append(messages, model.Message{Role: m.Role, Content: m.Content})
	}
	return messages
}
