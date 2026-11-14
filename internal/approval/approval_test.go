package approval

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
	"github.com/epierre/nestor/internal/tools"
)

var actionID = regexp.MustCompile(`^[0-9a-f]{32}$`)

const testTTL = 24 * time.Hour

var (
	testArgs = json.RawMessage(`{"text": "Dentist at 3pm"}`)
	testUser = identity.UserID(1)
	other    = identity.UserID(2)
)

// harness wires a Gate to a fake store and a registry holding notify_self, which counts its sends.
type harness struct {
	st    *testutil.FakeStore
	gate  *Gate
	tool  tools.Tool
	now   time.Time
	sends int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{st: testutil.NewFakeStore(), now: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)}
	reg := tools.NewRegistry()
	sender := func(context.Context, identity.UserID, string) error {
		h.sends++
		return nil
	}
	if err := reg.Register(tools.NewNotifySelfTool(sender)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	tool, err := reg.Lookup("notify_self")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	h.tool = tool
	h.gate = New(h.st, reg, testTTL)
	h.gate.now = func() time.Time { return h.now }
	return h
}

func (h *harness) propose(t *testing.T, user identity.UserID) store.PendingAction {
	t.Helper()
	action, err := h.gate.Propose(identity.WithUser(context.Background(), user), user, h.tool, testArgs)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	return action
}

func (h *harness) decide(user identity.UserID, id string, decision Decision) (store.PendingAction, error) {
	return h.gate.Decide(identity.WithUser(context.Background(), user), user, id, decision)
}

func (h *harness) stored(t *testing.T, user identity.UserID, id string) store.PendingAction {
	t.Helper()
	for _, a := range h.st.PendingActions(user) {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("no stored action %s for user %d", id, user)
	return store.PendingAction{}
}

func TestProposeStoresPendingAction(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)

	stored := h.stored(t, testUser, action.ID)
	if !actionID.MatchString(stored.ID) {
		t.Errorf("ID %q is not 32 lowercase hex digits", stored.ID)
	}
	if stored.UserID != testUser || stored.ToolName != "notify_self" {
		t.Errorf("owner or tool: got user %d tool %q", stored.UserID, stored.ToolName)
	}
	if string(stored.ArgsJSON) != string(testArgs) {
		t.Errorf("args: got %s, want %s", stored.ArgsJSON, testArgs)
	}
	preview, err := h.tool.Preview(testArgs)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if stored.Preview != preview {
		t.Errorf("preview: got %q, want %q", stored.Preview, preview)
	}
	if stored.State != store.PendingProposed {
		t.Errorf("state: got %q, want proposed", stored.State)
	}
	if stored.ArgsHash != ArgsHash(testUser, "notify_self", testArgs) {
		t.Error("args hash does not match ArgsHash(user, tool, args)")
	}
	if stored.ExpiresAt.Sub(stored.CreatedAt) != testTTL {
		t.Errorf("expiry: got %v after creation, want %v", stored.ExpiresAt.Sub(stored.CreatedAt), testTTL)
	}
}

func TestArgsHashBindsUserToolAndArgs(t *testing.T) {
	base := ArgsHash(testUser, "notify_self", testArgs)

	tests := []struct {
		name string
		hash string
	}{
		{"other user", ArgsHash(2, "notify_self", testArgs)},
		{"other tool", ArgsHash(testUser, "other_tool", testArgs)},
		{"other args", ArgsHash(testUser, "notify_self", json.RawMessage(`{"text": "Dentist at 4pm"}`))},
		{"reformatted args", ArgsHash(testUser, "notify_self", json.RawMessage(`{"text":"Dentist at 3pm"}`))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.hash == base {
				t.Errorf("hash unchanged when %s", tt.name)
			}
		})
	}
	if ArgsHash(testUser, "notify_self", testArgs) != base {
		t.Error("hash is not stable for identical input")
	}
}

func TestProposeRefusesNonCritical(t *testing.T) {
	h := newHarness(t)
	ctx := identity.WithUser(context.Background(), testUser)

	_, err := h.gate.Propose(ctx, testUser, tools.NewCurrentTimeTool(), json.RawMessage(`{}`))
	if !errors.Is(err, ErrNotCritical) {
		t.Errorf("err = %v, want ErrNotCritical", err)
	}
	if n := len(h.st.PendingActions(testUser)); n != 0 {
		t.Errorf("stored %d pending actions, want 0", n)
	}
}

func TestProposeIDsNotSequential(t *testing.T) {
	h := newHarness(t)
	first := h.propose(t, testUser)
	second := h.propose(t, testUser)

	if first.ID == second.ID {
		t.Fatal("two proposals share an ID")
	}
	a, _ := new(big.Int).SetString(first.ID, 16)
	b, _ := new(big.Int).SetString(second.ID, 16)
	if new(big.Int).Sub(b, a).CmpAbs(big.NewInt(1)) == 0 {
		t.Errorf("IDs %s and %s are adjacent", first.ID, second.ID)
	}
}

func TestApproveExecutesOnce(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)

	t.Run("approve executes once", func(t *testing.T) {
		got, err := h.decide(testUser, action.ID, Approve)
		if err != nil {
			t.Fatalf("Decide: %v", err)
		}
		if got.State != store.PendingExecuted {
			t.Errorf("state: got %q, want executed", got.State)
		}
		if h.sends != 1 {
			t.Errorf("tool ran %d times, want 1", h.sends)
		}
		if stored := h.stored(t, testUser, action.ID); stored.State != store.PendingExecuted || stored.ExecutedAt == nil {
			t.Errorf("stored state %q executed_at %v, want executed with a time", stored.State, stored.ExecutedAt)
		}
	})

	t.Run("double tap executes once", func(t *testing.T) {
		_, err := h.decide(testUser, action.ID, Approve)
		if !errors.Is(err, ErrNoPendingAction) {
			t.Errorf("second tap: err = %v, want ErrNoPendingAction", err)
		}
		if h.sends != 1 {
			t.Errorf("tool ran %d times after double tap, want 1", h.sends)
		}
	})
}

func TestExpiredNotExecuted(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	h.now = h.now.Add(testTTL + time.Second)

	_, err := h.decide(testUser, action.ID, Approve)
	if !errors.Is(err, ErrExpired) {
		t.Errorf("err = %v, want ErrExpired", err)
	}
	if h.sends != 0 {
		t.Errorf("tool ran %d times after expiry, want 0", h.sends)
	}
	if state := h.stored(t, testUser, action.ID).State; state != store.PendingExpired {
		t.Errorf("state: got %q, want expired", state)
	}
}

func TestTamperedArgsRefused(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	h.st.TamperPendingAction(action.ID, json.RawMessage(`{"text": "Pay everyone"}`))

	_, err := h.decide(testUser, action.ID, Approve)
	if !errors.Is(err, ErrIntegrity) {
		t.Errorf("err = %v, want ErrIntegrity", err)
	}
	if h.sends != 0 {
		t.Errorf("tool ran %d times on tampered args, want 0", h.sends)
	}
	stored := h.stored(t, testUser, action.ID)
	if stored.State != store.PendingFailed || stored.Error == "" {
		t.Errorf("stored state %q error %q, want failed with a message", stored.State, stored.Error)
	}
}

func TestOtherUsersActionBehavesLikeUnknown(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)

	t.Run("other user's approval", func(t *testing.T) {
		_, err := h.decide(other, action.ID, Approve)
		if !errors.Is(err, ErrNoPendingAction) {
			t.Errorf("err = %v, want ErrNoPendingAction", err)
		}
		if state := h.stored(t, testUser, action.ID).State; state != store.PendingProposed {
			t.Errorf("owner's action state %q, want proposed", state)
		}
	})

	t.Run("stranger callback with unknown ID", func(t *testing.T) {
		_, err := h.decide(other, "00000000000000000000000000000000", Approve)
		if !errors.Is(err, ErrNoPendingAction) {
			t.Errorf("err = %v, want ErrNoPendingAction", err)
		}
	})

	if h.sends != 0 {
		t.Errorf("tool ran %d times for a stranger, want 0", h.sends)
	}
}

func TestRejectNeverExecutes(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)

	got, err := h.decide(testUser, action.ID, Reject)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if got.State != store.PendingRejected {
		t.Errorf("state: got %q, want rejected", got.State)
	}
	if h.sends != 0 {
		t.Errorf("tool ran %d times after rejection, want 0", h.sends)
	}
	if _, err := h.decide(testUser, action.ID, Approve); !errors.Is(err, ErrNoPendingAction) {
		t.Errorf("approve after reject: err = %v, want ErrNoPendingAction", err)
	}
}

// approveButStall records an approval without running it, as a crash after the claim would leave it.
func (h *harness) approveButStall(t *testing.T, user identity.UserID, id string) {
	t.Helper()
	claimed := h.gate.instant()
	ok, err := h.st.UpdatePendingAction(identity.WithUser(context.Background(), user), user, id,
		store.PendingProposed, store.PendingUpdate{State: store.PendingApproved, DecidedAt: &claimed})
	if err != nil || !ok {
		t.Fatalf("stall approval: ok=%v err=%v", ok, err)
	}
}

func TestStaleApprovedFailedAsUnknown(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	h.approveButStall(t, testUser, action.ID)

	n, err := SweepStale(context.Background(), h.st, h.now.Add(10*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d actions, want 1", n)
	}
	stored := h.stored(t, testUser, action.ID)
	if stored.State != store.PendingFailed || stored.Error != ErrOutcomeUnknown.Error() {
		t.Errorf("state %q error %q, want failed with the outcome-unknown error", stored.State, stored.Error)
	}
}

func TestFreshApprovedLeftAlone(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	h.approveButStall(t, testUser, action.ID)

	n, err := SweepStale(context.Background(), h.st, h.now.Add(time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 0 {
		t.Errorf("swept %d actions, want 0", n)
	}
	if state := h.stored(t, testUser, action.ID).State; state != store.PendingApproved {
		t.Errorf("state %q, want approved", state)
	}
}

func TestStaleApprovedNeverReExecuted(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	h.approveButStall(t, testUser, action.ID)
	if _, err := SweepStale(context.Background(), h.st, h.now.Add(10*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	if _, err := h.decide(testUser, action.ID, Approve); !errors.Is(err, ErrNoPendingAction) {
		t.Errorf("approve after sweep: err = %v, want ErrNoPendingAction", err)
	}
	if h.sends != 0 {
		t.Errorf("tool ran %d times, want 0", h.sends)
	}
}

// systemNotes returns the system messages in the user's open conversation, which is where the
// model reads outcomes from.
func (h *harness) systemNotes(t *testing.T, user identity.UserID) []string {
	t.Helper()
	ctx := identity.WithUser(context.Background(), user)
	conv, err := h.st.ActiveConversation(ctx, user)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	msgs, err := h.st.RecentMessages(ctx, user, conv.ID, 100)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	var notes []string
	for _, m := range msgs {
		if m.Role == model.RoleSystem {
			notes = append(notes, m.Content)
		}
	}
	return notes
}

func TestOutcomeRecordedInConversation(t *testing.T) {
	tests := []struct {
		name     string
		run      func(t *testing.T, h *harness, id string)
		contains string
	}{
		{"rejected", func(t *testing.T, h *harness, id string) {
			if _, err := h.decide(testUser, id, Reject); err != nil {
				t.Fatalf("Decide: %v", err)
			}
		}, "was rejected"},
		{"executed", func(t *testing.T, h *harness, id string) {
			if _, err := h.decide(testUser, id, Approve); err != nil {
				t.Fatalf("Decide: %v", err)
			}
		}, "was approved and ran"},
		{"expired", func(t *testing.T, h *harness, id string) {
			h.now = h.now.Add(testTTL + time.Second)
			if _, err := h.decide(testUser, id, Approve); !errors.Is(err, ErrExpired) {
				t.Fatalf("Decide: %v, want ErrExpired", err)
			}
		}, "expired"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			action := h.propose(t, testUser)
			tt.run(t, h, action.ID)

			notes := h.systemNotes(t, testUser)
			if len(notes) != 1 {
				t.Fatalf("got %d system notes %q, want 1", len(notes), notes)
			}
			if !strings.Contains(notes[0], action.ID) || !strings.Contains(notes[0], tt.contains) {
				t.Errorf("note %q, want the action ID and %q", notes[0], tt.contains)
			}
			if strings.Contains(notes[0], "Dentist") {
				t.Errorf("note %q carries arguments; it must name only the action and outcome", notes[0])
			}
		})
	}
}

func TestNewProposalSupersedesEarlierOfSameTool(t *testing.T) {
	h := newHarness(t)
	first := h.propose(t, testUser)
	second := h.propose(t, testUser)

	if state := h.stored(t, testUser, first.ID).State; state != store.PendingSuperseded {
		t.Errorf("earlier action state %q, want superseded", state)
	}
	if got := h.stored(t, testUser, first.ID).SupersededBy; got != second.ID {
		t.Errorf("superseded_by %q, want %s", got, second.ID)
	}
	if state := h.stored(t, testUser, second.ID).State; state != store.PendingProposed {
		t.Errorf("replacement state %q, want proposed", state)
	}

	if _, err := h.decide(testUser, first.ID, Approve); !errors.Is(err, ErrNoPendingAction) {
		t.Errorf("approving superseded action: err = %v, want ErrNoPendingAction", err)
	}
	if h.sends != 0 {
		t.Errorf("tool ran %d times, want 0 (replacement needs its own approval)", h.sends)
	}
}

func TestSupersedeLeavesOtherUsersActionsAlone(t *testing.T) {
	h := newHarness(t)
	theirs := h.propose(t, other)
	h.propose(t, testUser)

	if state := h.stored(t, other, theirs.ID).State; state != store.PendingProposed {
		t.Errorf("other user's action state %q, want proposed", state)
	}
}

func TestUnsentCardsAreRetriedUntilMarked(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	ctx := identity.WithUser(context.Background(), testUser)

	unsent, err := h.st.UnsentCards(ctx, testUser, h.now)
	if err != nil {
		t.Fatalf("UnsentCards: %v", err)
	}
	if len(unsent) != 1 || unsent[0].ID != action.ID {
		t.Fatalf("unsent %+v, want the new action", unsent)
	}
	if err := h.st.MarkCardSent(ctx, testUser, action.ID, h.now); err != nil {
		t.Fatalf("MarkCardSent: %v", err)
	}
	unsent, _ = h.st.UnsentCards(ctx, testUser, h.now)
	if len(unsent) != 0 {
		t.Errorf("unsent after marking %+v, want none", unsent)
	}
}

func TestExpiredActionsGetNoCardRetry(t *testing.T) {
	h := newHarness(t)
	h.propose(t, testUser)
	ctx := identity.WithUser(context.Background(), testUser)

	unsent, err := h.st.UnsentCards(ctx, testUser, h.now.Add(testTTL+time.Second))
	if err != nil {
		t.Fatalf("UnsentCards: %v", err)
	}
	if len(unsent) != 0 {
		t.Errorf("unsent %+v, want none after expiry", unsent)
	}
}
