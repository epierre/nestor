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
	Model        string // proxy alias
	SystemPrompt string
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

// HandleMessage returns the model's reply to text for the user carried by ctx.
func (a *Agent) HandleMessage(ctx context.Context, text string) (string, error) {
	if _, err := identity.UserFrom(ctx); err != nil {
		return "", fmt.Errorf("handle message: %w", err)
	}

	resp, err := a.client.Complete(ctx, model.CompletionRequest{
		Model: a.opts.Model,
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: a.opts.SystemPrompt},
			{Role: model.RoleUser, Content: text},
		},
	})
	if err != nil {
		return "", fmt.Errorf("complete: %w", err)
	}
	return resp.Content, nil
}
