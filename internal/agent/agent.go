// Package agent turns a user's message into a reply.
package agent

import (
	"context"
	"fmt"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
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
// stores the model's reply.
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

	resp, err := a.client.Complete(ctx, model.CompletionRequest{
		Model:    a.opts.Model,
		Messages: a.prompt(history),
	})
	if err != nil {
		return "", fmt.Errorf("complete: %w", err)
	}
	if _, err := a.store.AppendMessage(ctx, user, conversation.ID, model.RoleAssistant, resp.Content); err != nil {
		return "", fmt.Errorf("store reply: %w", err)
	}
	return resp.Content, nil
}

func (a *Agent) prompt(history []store.Message) []model.Message {
	messages := make([]model.Message, 0, len(history)+1)
	messages = append(messages, model.Message{Role: model.RoleSystem, Content: a.opts.SystemPrompt})
	for _, m := range history {
		messages = append(messages, model.Message{Role: m.Role, Content: m.Content})
	}
	return messages
}
