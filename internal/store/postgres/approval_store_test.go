//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

func TestProposeStoresHashAndExpiry(t *testing.T) {
	st, pool, a, _, ctxA, _ := twoScoped(t)
	ttl := 24 * time.Hour
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })

	action, err := approval.New(st, ttl).Propose(ctxA, a, tool, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	var got struct {
		userID    int64
		toolName  string
		argsJSON  string
		argsHash  string
		preview   string
		state     string
		createdAt time.Time
		expiresAt time.Time
	}
	err = pool.QueryRow(context.Background(), `
		SELECT user_id, tool_name, args_json::text, args_hash, preview, state, created_at, expires_at
		FROM pending_actions WHERE id = $1`, action.ID).
		Scan(&got.userID, &got.toolName, &got.argsJSON, &got.argsHash, &got.preview, &got.state, &got.createdAt, &got.expiresAt)
	if err != nil {
		t.Fatalf("query pending action: %v", err)
	}
	if got.userID != int64(a) {
		t.Errorf("user_id: got %d, want %d", got.userID, a)
	}
	if got.toolName != "notify_self" {
		t.Errorf("tool_name: got %q, want notify_self", got.toolName)
	}
	if got.argsJSON != string(args) {
		t.Errorf("args_json: got %s, want %s (stored text must match the hashed bytes)", got.argsJSON, args)
	}
	if want := approval.ArgsHash(a, "notify_self", args); got.argsHash != want {
		t.Errorf("args_hash: got %s, want %s", got.argsHash, want)
	}
	if got.preview == "" {
		t.Error("preview is empty")
	}
	if got.state != "proposed" {
		t.Errorf("state: got %q, want proposed", got.state)
	}
	if d := got.expiresAt.Sub(got.createdAt); d != ttl {
		t.Errorf("expires_at - created_at: got %v, want %v", d, ttl)
	}
}

func TestProposeIDsNotSequential(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	gate := approval.New(st, time.Hour)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)

	first, err := gate.Propose(ctxA, a, tool, args)
	if err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	second, err := gate.Propose(ctxA, a, tool, args)
	if err != nil {
		t.Fatalf("second Propose: %v", err)
	}
	x, _ := new(big.Int).SetString(first.ID, 16)
	y, _ := new(big.Int).SetString(second.ID, 16)
	if x == nil || y == nil {
		t.Fatalf("IDs %q and %q are not hex", first.ID, second.ID)
	}
	if new(big.Int).Sub(y, x).CmpAbs(big.NewInt(1)) == 0 {
		t.Errorf("IDs %s and %s are adjacent", first.ID, second.ID)
	}
}
