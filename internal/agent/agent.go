// Package agent turns a user's message into a reply.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

const (
	noticeModelFailure = "I couldn't reach the model, try again shortly."
	noticeLoopLimit    = "I couldn't finish that request, try asking in a different way."

	commandNew            = "/new"
	noticeNewConversation = "Started a new conversation."

	toolsUnavailable = "error: no tools are available"
)

// Options configures an Agent.
type Options struct {
	Model         string // proxy alias
	SystemPrompt  string
	HistoryLimit  int // messages of the conversation sent to the model
	MaxIterations int // model calls allowed for one message
}

// Agent runs the conversation loop for one message at a time.
type Agent struct {
	client model.ModelClient
	store  store.Store
	opts   Options
}

// New returns an Agent using client and st.
func New(client model.ModelClient, st store.Store, opts Options) *Agent {
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
	for range a.opts.MaxIterations {
		resp, callErr := a.client.Complete(ctx, model.CompletionRequest{Model: a.opts.Model, Messages: messages})
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
		messages = appendToolRefusals(messages, resp)
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

func (a *Agent) prompt(history []store.Message) []model.Message {
	messages := make([]model.Message, 0, len(history)+1)
	messages = append(messages, model.Message{Role: model.RoleSystem, Content: a.opts.SystemPrompt})
	for _, m := range history {
		messages = append(messages, model.Message{Role: m.Role, Content: m.Content})
	}
	return messages
}
