// Package agent turns a user's message into a reply.
package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

const (
	noticeModelFailure = "I couldn't reach the model, try again shortly."
	noticeLoopLimit    = "I couldn't finish that request, try asking in a different way."

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
// not be handled at all, such as when the store is down.
func (a *Agent) HandleMessage(ctx context.Context, text string) (string, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return "", fmt.Errorf("handle message: %w", err)
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
		resp, err := a.client.Complete(ctx, model.CompletionRequest{Model: a.opts.Model, Messages: messages})
		if err != nil {
			slog.ErrorContext(ctx, "model call failed", "error", err)
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
