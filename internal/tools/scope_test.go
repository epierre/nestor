package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/epierre/nestor/internal/identity"
)

// criticalScoped is a critical tool enabled for user 1 only.
type criticalScoped struct{}

func (criticalScoped) Name() string                            { return "weather__delete_task" }
func (criticalScoped) Description() string                     { return "critical, one user" }
func (criticalScoped) Schema() json.RawMessage                 { return json.RawMessage(`{"type":"object"}`) }
func (criticalScoped) Risk() Risk                              { return RiskCritical }
func (criticalScoped) Preview(json.RawMessage) (string, error) { return "", nil }
func (criticalScoped) EnabledFor(user identity.UserID) bool    { return user == 1 }
func (criticalScoped) Execute(context.Context, json.RawMessage) (Result, error) {
	return Result{}, nil
}

func TestAgentViewKeepsUserScope(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(criticalScoped{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tool, err := reg.AgentView().Lookup("weather__delete_task")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if Enabled(tool, 2) {
		t.Error("critical tool enabled for user 2 through the agent view; scope was lost")
	}
	if !Enabled(tool, 1) {
		t.Error("critical tool not enabled for user 1 through the agent view")
	}
}
