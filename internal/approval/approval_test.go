package approval

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

var actionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func noopSender(context.Context, identity.UserID, string) error { return nil }

func TestProposeStoresPendingAction(t *testing.T) {
	const user identity.UserID = 1
	ctx := identity.WithUser(context.Background(), user)
	st := testutil.NewFakeStore()
	tool := tools.NewNotifySelfTool(noopSender)
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)
	ttl := 24 * time.Hour

	g := New(st, ttl)
	action, err := g.Propose(ctx, user, tool, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	preview, err := tool.Preview(args)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	stored := st.PendingActions(user)
	if len(stored) != 1 {
		t.Fatalf("stored %d pending actions for user, want 1", len(stored))
	}
	got := stored[0]
	if got.ID != action.ID || !actionID.MatchString(got.ID) {
		t.Errorf("ID: got %q, want the returned ID in 32 lowercase hex digits", got.ID)
	}
	if got.UserID != user || got.ToolName != "notify_self" {
		t.Errorf("owner or tool: got user %d tool %q", got.UserID, got.ToolName)
	}
	if string(got.ArgsJSON) != string(args) {
		t.Errorf("args: got %s, want %s", got.ArgsJSON, args)
	}
	if got.Preview != preview {
		t.Errorf("preview: got %q, want %q", got.Preview, preview)
	}
	if got.State != "proposed" {
		t.Errorf("state: got %q, want proposed", got.State)
	}
	if got.ArgsHash != ArgsHash(user, "notify_self", args) {
		t.Errorf("args hash does not match ArgsHash(user, tool, args)")
	}
	if got.ExpiresAt.Sub(got.CreatedAt) != ttl {
		t.Errorf("expiry: got %v after creation, want %v", got.ExpiresAt.Sub(got.CreatedAt), ttl)
	}
}

func TestArgsHashBindsUserToolAndArgs(t *testing.T) {
	const user identity.UserID = 1
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)
	base := ArgsHash(user, "notify_self", args)

	tests := []struct {
		name string
		hash string
	}{
		{"other user", ArgsHash(2, "notify_self", args)},
		{"other tool", ArgsHash(user, "other_tool", args)},
		{"other args", ArgsHash(user, "notify_self", json.RawMessage(`{"text": "Dentist at 4pm"}`))},
		{"reformatted args", ArgsHash(user, "notify_self", json.RawMessage(`{"text":"Dentist at 3pm"}`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hash == base {
				t.Errorf("hash unchanged when %s", tt.name)
			}
		})
	}
	if ArgsHash(user, "notify_self", args) != base {
		t.Error("hash is not stable for identical input")
	}
}

func TestProposeRefusesNonCritical(t *testing.T) {
	const user identity.UserID = 1
	ctx := identity.WithUser(context.Background(), user)
	st := testutil.NewFakeStore()

	_, err := New(st, time.Hour).Propose(ctx, user, tools.NewCurrentTimeTool(), json.RawMessage(`{}`))
	if !errors.Is(err, ErrNotCritical) {
		t.Errorf("err = %v, want ErrNotCritical", err)
	}
	if n := len(st.PendingActions(user)); n != 0 {
		t.Errorf("stored %d pending actions, want 0", n)
	}
}

func TestProposeIDsNotSequential(t *testing.T) {
	const user identity.UserID = 1
	ctx := identity.WithUser(context.Background(), user)
	st := testutil.NewFakeStore()
	g := New(st, time.Hour)
	tool := tools.NewNotifySelfTool(noopSender)
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)

	first, err := g.Propose(ctx, user, tool, args)
	if err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	second, err := g.Propose(ctx, user, tool, args)
	if err != nil {
		t.Fatalf("second Propose: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("two proposals share an ID")
	}
	a, _ := new(big.Int).SetString(first.ID, 16)
	b, _ := new(big.Int).SetString(second.ID, 16)
	if new(big.Int).Abs(new(big.Int).Sub(a, b)).Cmp(big.NewInt(1)) == 0 {
		t.Errorf("IDs %s and %s are adjacent", first.ID, second.ID)
	}
}
