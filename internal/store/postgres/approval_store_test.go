//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/approval"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/tools"
)

func TestProposeStoresHashAndExpiry(t *testing.T) {
	st, pool, a, _, ctxA, _ := twoScoped(t)
	ttl := 24 * time.Hour
	args := json.RawMessage(`{"text": "Dentist at 3pm"}`)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })

	action, err := approval.New(st, tools.NewRegistry(), ttl).Propose(ctxA, a, tool, args)
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
	gate := approval.New(st, tools.NewRegistry(), time.Hour)
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

func TestGetPendingActionScopedToUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })
	action, err := approval.New(st, tools.NewRegistry(), time.Hour).Propose(ctxA, a, tool, json.RawMessage(`{"text": "hi"}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	got, err := st.GetPendingAction(ctxA, a, action.ID)
	if err != nil {
		t.Fatalf("GetPendingAction for owner: %v", err)
	}
	if got.ID != action.ID || got.State != store.PendingProposed {
		t.Errorf("got %s in state %q, want %s proposed", got.ID, got.State, action.ID)
	}

	_, err = st.GetPendingAction(ctxB, b, action.ID)
	if !errors.Is(err, store.ErrActionNotFound) {
		t.Errorf("other user: err = %v, want ErrActionNotFound", err)
	}
}

func TestUpdatePendingActionOnlyFromExpectedState(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })
	action, err := approval.New(st, tools.NewRegistry(), time.Hour).Propose(ctxA, a, tool, json.RawMessage(`{"text": "hi"}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	decided := time.Now().UTC().Truncate(time.Microsecond)
	update := store.PendingUpdate{State: store.PendingRejected, DecidedAt: &decided}

	if ok, err := st.UpdatePendingAction(ctxB, b, action.ID, store.PendingProposed, update); err != nil || ok {
		t.Errorf("other user: got (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := st.UpdatePendingAction(ctxA, a, action.ID, store.PendingProposed, update); err != nil || !ok {
		t.Fatalf("owner from proposed: got (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := st.UpdatePendingAction(ctxA, a, action.ID, store.PendingProposed, update); err != nil || ok {
		t.Errorf("owner from proposed again: got (%v, %v), want (false, nil)", ok, err)
	}

	got, err := st.GetPendingAction(ctxA, a, action.ID)
	if err != nil {
		t.Fatalf("GetPendingAction: %v", err)
	}
	if got.State != store.PendingRejected || got.DecidedAt == nil || !got.DecidedAt.Equal(decided) {
		t.Errorf("state %q decided_at %v, want rejected at %v", got.State, got.DecidedAt, decided)
	}
}

func TestFailStaleApprovalsTouchesOnlyStaleApproved(t *testing.T) {
	st, pool, a, b, ctxA, ctxB := twoScoped(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	old := now.Add(-time.Hour)

	// Rows go in directly, each with its own tool, so that one pending action per tool does not
	// supersede the others.
	made := 0
	propose := func(ctx context.Context, user identity.UserID) store.PendingAction {
		made++
		action := store.PendingAction{
			ID:        fmt.Sprintf("sweep-%d", made),
			ToolName:  fmt.Sprintf("tool_%d", made),
			ArgsJSON:  json.RawMessage(`{"text": "hi"}`),
			ArgsHash:  "unused",
			Preview:   "hi",
			State:     store.PendingProposed,
			CreatedAt: now,
			ExpiresAt: now.Add(time.Hour),
		}
		if err := st.ProposeAction(ctx, user, action); err != nil {
			t.Fatalf("ProposeAction: %v", err)
		}
		return action
	}
	approve := func(ctx context.Context, user identity.UserID, id string, at time.Time) {
		ok, err := st.UpdatePendingAction(ctx, user, id, store.PendingProposed,
			store.PendingUpdate{State: store.PendingApproved, DecidedAt: &at})
		if err != nil || !ok {
			t.Fatalf("approve %s: ok=%v err=%v", id, ok, err)
		}
	}
	staleA, freshA, proposedA := propose(ctxA, a), propose(ctxA, a), propose(ctxA, a)
	staleB := propose(ctxB, b)
	approve(ctxA, a, staleA.ID, old)
	approve(ctxA, a, freshA.ID, now)
	approve(ctxB, b, staleB.ID, old)

	n, err := approval.SweepStale(context.Background(), st, nil, now, 5*time.Minute)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 2 {
		t.Errorf("swept %d actions, want 2 (one per user)", n)
	}

	want := map[string]string{
		staleA.ID:    store.PendingFailed,
		freshA.ID:    store.PendingApproved,
		proposedA.ID: store.PendingProposed,
		staleB.ID:    store.PendingFailed,
	}
	for id, state := range want {
		var got string
		if err := pool.QueryRow(context.Background(), `SELECT state FROM pending_actions WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		if got != state {
			t.Errorf("action %s state %q, want %q", id, got, state)
		}
	}
}

func TestSupersedeAndCardRetryAreScopedToUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })
	gate := approval.New(st, tools.NewRegistry(), time.Hour)
	args := json.RawMessage(`{"text": "hi"}`)

	theirs, err := gate.Propose(ctxB, b, tool, args)
	if err != nil {
		t.Fatalf("Propose for B: %v", err)
	}
	first, err := gate.Propose(ctxA, a, tool, args)
	if err != nil {
		t.Fatalf("Propose for A: %v", err)
	}
	second, err := gate.Propose(ctxA, a, tool, args)
	if err != nil {
		t.Fatalf("second Propose for A: %v", err)
	}

	got, err := st.GetPendingAction(ctxA, a, first.ID)
	if err != nil {
		t.Fatalf("GetPendingAction: %v", err)
	}
	if got.State != store.PendingSuperseded || got.SupersededBy != second.ID {
		t.Errorf("A's first action state %q superseded_by %q, want superseded by %s", got.State, got.SupersededBy, second.ID)
	}
	if got, _ := st.GetPendingAction(ctxB, b, theirs.ID); got.State != store.PendingProposed {
		t.Errorf("B's action state %q, want proposed", got.State)
	}

	unsent, err := st.UnsentCards(ctxA, a, time.Now())
	if err != nil {
		t.Fatalf("UnsentCards: %v", err)
	}
	if len(unsent) != 1 || unsent[0].ID != second.ID {
		t.Errorf("A's unsent cards %+v, want only the replacement", unsent)
	}
	if err := st.MarkCardSent(ctxB, b, second.ID, 0, time.Now()); !errors.Is(err, store.ErrActionNotFound) {
		t.Errorf("marking A's card as B: err = %v, want ErrActionNotFound", err)
	}
}

func TestCardDeliveryRecordsChat(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	tool := tools.NewNotifySelfTool(func(context.Context, identity.UserID, string) error { return nil })
	action, err := approval.New(st, tools.NewRegistry(), time.Hour).Propose(ctxA, a, tool, json.RawMessage(`{"text": "hi"}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}

	if err := st.MarkCardSent(ctxA, a, action.ID, 555, time.Now()); err != nil {
		t.Fatalf("MarkCardSent: %v", err)
	}
	got, err := st.GetPendingAction(ctxA, a, action.ID)
	if err != nil {
		t.Fatalf("GetPendingAction: %v", err)
	}
	if got.NotifyChatID != 555 || got.CardSentAt == nil {
		t.Errorf("chat %d card sent %v, want chat 555 and a delivery time", got.NotifyChatID, got.CardSentAt)
	}
}

// staleApprovals proposes one approved row per user, decided an hour ago, each in a chat of its own.
func staleApprovals(t *testing.T, st *Store, a, b identity.UserID, ctxA, ctxB context.Context) (previewA, previewB string, idA, idB string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Microsecond)
	mk := func(ctx context.Context, user identity.UserID, id, preview string, chat int64) {
		action := store.PendingAction{
			ID: id, ToolName: "tool_" + id, ArgsJSON: json.RawMessage(`{}`), ArgsHash: "unused",
			Preview: preview, State: store.PendingProposed, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
		}
		if err := st.ProposeAction(ctx, user, action); err != nil {
			t.Fatalf("ProposeAction: %v", err)
		}
		if err := st.MarkCardSent(ctx, user, id, chat, now); err != nil {
			t.Fatalf("MarkCardSent: %v", err)
		}
		decided := now.Add(-time.Hour)
		if ok, err := st.UpdatePendingAction(ctx, user, id, store.PendingProposed,
			store.PendingUpdate{State: store.PendingApproved, DecidedAt: &decided}); err != nil || !ok {
			t.Fatalf("approve %s: ok=%v err=%v", id, ok, err)
		}
	}
	mk(ctxA, a, "stale-a", "Dentist at 3pm", 501)
	mk(ctxB, b, "stale-b", "Gym at 7am", 502)
	return "Dentist at 3pm", "Gym at 7am", "stale-a", "stale-b"
}

func TestSweepReturnsStaleRowsWithTheirOwnChats(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	staleApprovals(t, st, a, b, ctxA, ctxB)

	failed, err := st.FailStaleApprovals(context.Background(), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("FailStaleApprovals: %v", err)
	}
	want := map[string]store.FailedApproval{
		"stale-a": {ID: "stale-a", UserID: a, Preview: "Dentist at 3pm", ChatID: 501},
		"stale-b": {ID: "stale-b", UserID: b, Preview: "Gym at 7am", ChatID: 502},
	}
	if len(failed) != len(want) {
		t.Fatalf("got %d failed approvals, want %d: %+v", len(failed), len(want), failed)
	}
	for _, f := range failed {
		if w, ok := want[f.ID]; !ok || f != w {
			t.Errorf("failed approval %+v, want %+v", f, want[f.ID])
		}
	}
}

func TestUnnotifiedOutcomesPerUserUntilMarked(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	staleApprovals(t, st, a, b, ctxA, ctxB)
	if _, err := st.FailStaleApprovals(context.Background(), time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("FailStaleApprovals: %v", err)
	}

	listA, err := st.UnnotifiedOutcomes(ctxA, a)
	if err != nil {
		t.Fatalf("UnnotifiedOutcomes A: %v", err)
	}
	if len(listA) != 1 || listA[0].ID != "stale-a" || listA[0].NotifyChatID != 501 {
		t.Errorf("A's unnotified outcomes %+v, want only stale-a in chat 501", listA)
	}
	listB, _ := st.UnnotifiedOutcomes(ctxB, b)
	if len(listB) != 1 || listB[0].ID != "stale-b" {
		t.Errorf("B's unnotified outcomes %+v, want only stale-b", listB)
	}

	if err := st.MarkOutcomeNoticed(ctxA, a, "stale-a", time.Now()); err != nil {
		t.Fatalf("MarkOutcomeNoticed: %v", err)
	}
	listA, _ = st.UnnotifiedOutcomes(ctxA, a)
	if len(listA) != 0 {
		t.Errorf("A's outcomes after marking %+v, want none", listA)
	}
	if err := st.MarkOutcomeNoticed(ctxB, b, "stale-a", time.Now()); !errors.Is(err, store.ErrActionNotFound) {
		t.Errorf("B marking A's outcome: err = %v, want ErrActionNotFound", err)
	}
}
