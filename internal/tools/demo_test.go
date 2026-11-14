package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

func TestDemoTools(t *testing.T) {
	t.Run("current_time tool", func(t *testing.T) {
		fixedTime := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
		tool := tools.NewCurrentTimeToolWithClock(func() time.Time { return fixedTime })

		if tool.Name() != "current_time" {
			t.Errorf("got name %q, want 'current_time'", tool.Name())
		}
		if tool.Risk() != tools.RiskRead {
			t.Errorf("got risk %q, want %q", tool.Risk(), tools.RiskRead)
		}

		// Preview
		preview, err := tool.Preview(json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Preview failed: %v", err)
		}
		if preview == "" {
			t.Error("expected non-empty preview")
		}

		// Execute
		res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		if !strings.Contains(res.Content, "2026-11-14") {
			t.Errorf("expected date in result, got: %q", res.Content)
		}
	})

	t.Run("notify_self tool", func(t *testing.T) {
		var sentUser identity.UserID
		var sentText string
		sender := func(ctx context.Context, uid identity.UserID, text string) error {
			sentUser = uid
			sentText = text
			return nil
		}

		tool := tools.NewNotifySelfTool(sender)

		if tool.Name() != "notify_self" {
			t.Errorf("got name %q, want 'notify_self'", tool.Name())
		}
		if tool.Risk() != tools.RiskCritical {
			t.Errorf("got risk %q, want %q", tool.Risk(), tools.RiskCritical)
		}

		// Preview
		preview, err := tool.Preview(json.RawMessage(`{"text": "Dentist at 3pm"}`))
		if err != nil {
			t.Fatalf("Preview failed: %v", err)
		}
		if !strings.Contains(preview, "Dentist at 3pm") {
			t.Errorf("expected preview to contain 'Dentist at 3pm', got %q", preview)
		}

		// Execute without user in context fails
		_, err = tool.Execute(context.Background(), json.RawMessage(`{"text": "Dentist at 3pm"}`))
		if err == nil {
			t.Fatal("expected error executing without user in context")
		}

		// Execute with user in context succeeds
		ctx := identity.WithUser(context.Background(), 42)
		res, err := tool.Execute(ctx, json.RawMessage(`{"text": "Dentist at 3pm"}`))
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		if sentUser != 42 || sentText != "Dentist at 3pm" {
			t.Errorf("sender called with (%v, %q), want (42, 'Dentist at 3pm')", sentUser, sentText)
		}
		if !strings.Contains(res.Content, "Dentist at 3pm") {
			t.Errorf("expected result to mention message, got %q", res.Content)
		}
	})

	t.Run("ENABLE_DEMO_TOOLS flag", func(t *testing.T) {
		rDisabled := tools.NewRegistry()
		if err := tools.RegisterDemoTools(rDisabled, false, nil); err != nil {
			t.Fatalf("RegisterDemoTools failed: %v", err)
		}
		if len(rDisabled.Tools()) != 0 {
			t.Errorf("expected 0 tools when disabled, got %d", len(rDisabled.Tools()))
		}

		rEnabled := tools.NewRegistry()
		if err := tools.RegisterDemoTools(rEnabled, true, nil); err != nil {
			t.Fatalf("RegisterDemoTools failed: %v", err)
		}
		modelTools := rEnabled.Tools()
		if len(modelTools) != 2 {
			t.Fatalf("expected 2 tools when enabled, got %d", len(modelTools))
		}
		names := map[string]bool{modelTools[0].Name: true, modelTools[1].Name: true}
		if !names["current_time"] || !names["notify_self"] {
			t.Errorf("expected current_time and notify_self, got %+v", modelTools)
		}
	})
}
