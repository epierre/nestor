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

	n, err := SweepStale(context.Background(), h.st, nil, h.now.Add(10*time.Minute), 5*time.Minute)
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

	n, err := SweepStale(context.Background(), h.st, nil, h.now.Add(time.Minute), 5*time.Minute)
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
	if _, err := SweepStale(context.Background(), h.st, nil, h.now.Add(10*time.Minute), 5*time.Minute); err != nil {
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

func TestSameTurnProposalsStayProposed(t *testing.T) {
	h := newHarness(t)
	turn1 := WithTurn(identity.WithUser(context.Background(), testUser))
	first, err := h.gate.Propose(turn1, testUser, h.tool, json.RawMessage(`{"text": "First"}`))
	if err != nil {
		t.Fatalf("first Propose: %v", err)
	}
	second, err := h.gate.Propose(turn1, testUser, h.tool, json.RawMessage(`{"text": "Second"}`))
	if err != nil {
		t.Fatalf("second Propose: %v", err)
	}

	if got := h.stored(t, testUser, first.ID).State; got != store.PendingProposed {
		t.Errorf("first same-turn state %q, want proposed", got)
	}
	if got := h.stored(t, testUser, second.ID).State; got != store.PendingProposed {
		t.Errorf("second same-turn state %q, want proposed", got)
	}

	// A replacement in a later turn supersedes both earlier proposals.
	turn2 := WithTurn(identity.WithUser(context.Background(), testUser))
	third, err := h.gate.Propose(turn2, testUser, h.tool, json.RawMessage(`{"text": "Replacement"}`))
	if err != nil {
		t.Fatalf("third Propose: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		got := h.stored(t, testUser, id)
		if got.State != store.PendingSuperseded || got.SupersededBy != third.ID {
			t.Errorf("action %s state %q superseded_by %q, want superseded by %s", id, got.State, got.SupersededBy, third.ID)
		}
		if _, err := h.decide(testUser, id, Approve); !errors.Is(err, ErrNoPendingAction) {
			t.Errorf("approving superseded %s: err = %v, want ErrNoPendingAction", id, err)
		}
	}
	if got := h.stored(t, testUser, third.ID).State; got != store.PendingProposed {
		t.Errorf("third state %q, want proposed", got)
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
	if err := h.st.MarkCardSent(ctx, testUser, action.ID, 0, h.now); err != nil {
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

func TestApproveAfterRestartExecutesOnce(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)

	// A restart is a new Gate over the same store, as in the agent tests.
	restarted := New(h.st, h.gate.tools, testTTL)
	restarted.now = h.gate.now
	got, err := restarted.Decide(identity.WithUser(context.Background(), testUser), testUser, action.ID, Approve)
	if err != nil {
		t.Fatalf("Decide after restart: %v", err)
	}
	if got.State != store.PendingExecuted || h.sends != 1 {
		t.Errorf("state %q, tool ran %d times; want executed once", got.State, h.sends)
	}
}

func TestExecutedActionKeepsItsAuditTrail(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	if _, err := h.decide(testUser, action.ID, Approve); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	stored := h.stored(t, testUser, action.ID)
	if stored.UserID != testUser || stored.State != store.PendingExecuted {
		t.Errorf("owner %d state %q, want user %d executed", stored.UserID, stored.State, testUser)
	}
	if stored.CreatedAt.IsZero() || stored.DecidedAt == nil || stored.ExecutedAt == nil {
		t.Errorf("timestamps proposed %v decided %v executed %v, want all set", stored.CreatedAt, stored.DecidedAt, stored.ExecutedAt)
	}
	if len(stored.ResultJSON) == 0 {
		t.Error("result not recorded")
	}
}

// fakeNotifier records the notices sent to chats, and fails them when err is set.
type fakeNotifier struct {
	sent []notice
	err  error
}

type notice struct {
	chat int64
	text string
}

func (f *fakeNotifier) Notify(_ context.Context, chatID int64, text string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, notice{chat: chatID, text: text})
	return nil
}

// stallWithCard proposes an action, records its card in chat, and leaves it approved without an
// outcome, as a crash after the claim would.
func (h *harness) stallWithCard(t *testing.T, user identity.UserID, chat int64) store.PendingAction {
	t.Helper()
	action := h.propose(t, user)
	ctx := identity.WithUser(context.Background(), user)
	if err := h.st.MarkCardSent(ctx, user, action.ID, chat, h.now); err != nil {
		t.Fatalf("MarkCardSent: %v", err)
	}
	h.approveButStall(t, user, action.ID)
	return action
}

func TestStaleApprovalNoticeSentToCardChat(t *testing.T) {
	h := newHarness(t)
	action := h.stallWithCard(t, testUser, 900)
	notifier := &fakeNotifier{}

	if _, err := SweepStale(context.Background(), h.st, notifier, h.now.Add(10*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	if len(notifier.sent) != 1 || notifier.sent[0].chat != 900 {
		t.Fatalf("notices %+v, want one to the card chat 900", notifier.sent)
	}
	text := notifier.sent[0].text
	if !strings.Contains(text, "Notify yourself: Dentist at 3pm") || !strings.Contains(text, "Outcome unknown") {
		t.Errorf("notice %q, want the preview and that the outcome is unknown", text)
	}
	if notes := h.systemNotes(t, testUser); len(notes) != 1 || !strings.Contains(notes[0], action.ID) {
		t.Errorf("conversation notes %q, want one note naming the action", notes)
	}
	if got := h.stored(t, testUser, action.ID); got.OutcomeNoticeSentAt == nil {
		t.Error("notice delivered but not marked as sent")
	}
}

func TestNoChatRecordedGetsNoteOnly(t *testing.T) {
	h := newHarness(t)
	action := h.propose(t, testUser)
	h.approveButStall(t, testUser, action.ID)
	notifier := &fakeNotifier{}

	if _, err := SweepStale(context.Background(), h.st, notifier, h.now.Add(10*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	if len(notifier.sent) != 0 {
		t.Errorf("sent %+v with no chat recorded, want nothing", notifier.sent)
	}
	if notes := h.systemNotes(t, testUser); len(notes) != 1 {
		t.Errorf("conversation notes %q, want the note", notes)
	}
}

func TestFailedNoticeStaysUnnotified(t *testing.T) {
	h := newHarness(t)
	action := h.stallWithCard(t, testUser, 900)
	notifier := &fakeNotifier{err: errors.New("telegram down")}

	if _, err := SweepStale(context.Background(), h.st, notifier, h.now.Add(10*time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("SweepStale: %v", err)
	}

	if got := h.stored(t, testUser, action.ID); got.State != store.PendingFailed || got.OutcomeNoticeSentAt != nil {
		t.Errorf("state %q noticed %v, want failed and not noticed", got.State, got.OutcomeNoticeSentAt)
	}
	ctx := identity.WithUser(context.Background(), testUser)
	if waiting, _ := h.st.UnnotifiedOutcomes(ctx, testUser); len(waiting) != 1 {
		t.Errorf("unnotified outcomes %+v, want the failed notice kept for the next message", waiting)
	}
}

// storedTitleTool is critical. Its generic preview names only the arguments, and its context
// preview names the stored title the arguments refer to.
type storedTitleTool struct{}

func (storedTitleTool) Name() string            { return "stored_title" }
func (storedTitleTool) Description() string     { return "Deletes a titled thing." }
func (storedTitleTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (storedTitleTool) Risk() tools.Risk        { return tools.RiskCritical }
func (storedTitleTool) Execute(context.Context, json.RawMessage) (tools.Result, error) {
	return tools.Result{Content: "gone"}, nil
}
func (storedTitleTool) Preview(json.RawMessage) (string, error) { return "Delete item 7", nil }
func (storedTitleTool) PreviewFor(ctx context.Context, _ json.RawMessage) (string, error) {
	if _, err := identity.UserFrom(ctx); err != nil {
		return "", err
	}
	return "Delete: Renew passport", nil
}

func TestProposalPreviewUsesStoredData(t *testing.T) {
	h := newHarness(t)
	ctx := identity.WithUser(context.Background(), testUser)
	action, err := h.gate.Propose(ctx, testUser, storedTitleTool{}, json.RawMessage(`{"id": 7}`))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if action.Preview != "Delete: Renew passport" {
		t.Errorf("preview %q, want the stored title from PreviewFor", action.Preview)
	}
}

// sweepSpyStore records per-user FailStaleApprovals calls and can fail one user's sweep.
type sweepSpyStore struct {
	*testutil.FakeStore
	failUser identity.UserID
	failErr  error
	calls    []identity.UserID
}

func (s *sweepSpyStore) FailStaleApprovals(ctx context.Context, user identity.UserID, cutoff time.Time) ([]store.FailedApproval, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	s.calls = append(s.calls, user)
	if user == s.failUser && s.failErr != nil {
		return nil, s.failErr
	}
	return s.FakeStore.FailStaleApprovals(ctx, user, cutoff)
}

func TestSweepScopesQueriesPerActiveUser(t *testing.T) {
	t.Run("scopes each user separately", func(t *testing.T) {
		h := newHarness(t)
		h.stallWithCard(t, testUser, 900)
		h.stallWithCard(t, other, 901)
		spy := &sweepSpyStore{FakeStore: h.st}
		notifier := &fakeNotifier{}

		n, err := SweepStale(context.Background(), spy, notifier, h.now.Add(10*time.Minute), 5*time.Minute)
		if err != nil {
			t.Fatalf("SweepStale: %v", err)
		}
		if n != 2 {
			t.Errorf("swept %d actions, want 2", n)
		}
		if len(spy.calls) != 2 || spy.calls[0] != testUser || spy.calls[1] != other {
			t.Errorf("FailStaleApprovals calls = %v, want [%d %d]", spy.calls, testUser, other)
		}
		if len(notifier.sent) != 2 || notifier.sent[0].chat != 900 || notifier.sent[1].chat != 901 {
			t.Errorf("notices %+v, want one per active user's chat", notifier.sent)
		}
	})

	t.Run("continues across active users when one user fails", func(t *testing.T) {
		h := newHarness(t)
		h.stallWithCard(t, testUser, 900)
		bAction := h.stallWithCard(t, other, 901)
		boom := errors.New("row lock timeout")
		spy := &sweepSpyStore{FakeStore: h.st, failUser: testUser, failErr: boom}
		notifier := &fakeNotifier{}

		n, err := SweepStale(context.Background(), spy, notifier, h.now.Add(10*time.Minute), 5*time.Minute)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		if n != 1 {
			t.Errorf("swept %d actions, want 1 for the healthy user", n)
		}
		if got := h.stored(t, other, bAction.ID); got.State != store.PendingFailed {
			t.Errorf("other user's action state %q, want failed", got.State)
		}
		if len(notifier.sent) != 1 || notifier.sent[0].chat != 901 {
			t.Errorf("notices %+v, want other user's notice sent to chat 901", notifier.sent)
		}
	})
}

func TestSweepSkipsDisabledUser(t *testing.T) {
	h := newHarness(t)
	h.st.AddIdentity("telegram", "100", testUser)
	h.st.AddIdentity("telegram", "200", other)
	disabledAction := h.stallWithCard(t, testUser, 900)
	activeAction := h.stallWithCard(t, other, 901)
	h.st.DisableIdentity("telegram", "100")
	notifier := &fakeNotifier{}

	n, err := SweepStale(context.Background(), h.st, notifier, h.now.Add(10*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatalf("SweepStale: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d actions, want 1 (only active user)", n)
	}
	if len(notifier.sent) != 1 || notifier.sent[0].chat != 901 {
		t.Errorf("notices %+v, want only active user's chat 901", notifier.sent)
	}
	if got := h.stored(t, testUser, disabledAction.ID); got.State != store.PendingApproved {
		t.Errorf("disabled user's action state %q, want untouched approved", got.State)
	}
	if got := h.stored(t, other, activeAction.ID); got.State != store.PendingFailed {
		t.Errorf("active user's action state %q, want failed", got.State)
	}
}
