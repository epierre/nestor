package reminders

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
)

// recordingNotifier records the messages sent to chats, and fails its first failures calls.
type recordingNotifier struct {
	failures int
	sent     []notice
}

type notice struct {
	chat int64
	text string
}

func (r *recordingNotifier) Notify(_ context.Context, chatID int64, text string) error {
	if r.failures > 0 {
		r.failures--
		return errors.New("telegram down")
	}
	r.sent = append(r.sent, notice{chat: chatID, text: text})
	return nil
}

const (
	other     = identity.UserID(2)
	otherChat = int64(777)
)

// addDue stores a reminder that became due a minute before now.
func addDue(t *testing.T, st *testutil.FakeStore, user identity.UserID, text string, chat int64) {
	t.Helper()
	ctx := identity.WithUser(context.Background(), user)
	r := store.Reminder{Text: text, DueAt: now.Add(-time.Minute), ChatID: chat}
	if _, err := st.AddReminder(ctx, user, r); err != nil {
		t.Fatalf("AddReminder: %v", err)
	}
}

func TestDueReminderSentToItsChat(t *testing.T) {
	st := testutil.NewFakeStore()
	addDue(t, st, owner, "Call the dentist", ownerChat)
	n := &recordingNotifier{}

	if err := deliverDue(context.Background(), st, n, now); err != nil {
		t.Fatalf("deliverDue: %v", err)
	}
	if len(n.sent) != 1 || n.sent[0].chat != ownerChat || n.sent[0].text != "Reminder: Call the dentist" {
		t.Errorf("sent %+v, want the reminder to chat %d", n.sent, ownerChat)
	}
	if due, _ := st.DueReminders(identity.WithUser(context.Background(), owner), owner, now); len(due) != 0 {
		t.Errorf("reminder still due after delivery: %+v", due)
	}
}

func TestFailedSendRetriedOnNextTick(t *testing.T) {
	st := testutil.NewFakeStore()
	addDue(t, st, owner, "Call the dentist", ownerChat)
	n := &recordingNotifier{failures: 1}

	if err := deliverDue(context.Background(), st, n, now); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if len(n.sent) != 0 {
		t.Fatalf("sent %+v despite the failure", n.sent)
	}
	if err := deliverDue(context.Background(), st, n, now); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if len(n.sent) != 1 {
		t.Errorf("sent %+v on the next tick, want the reminder", n.sent)
	}
}

func TestDeliveredReminderNotSentAgain(t *testing.T) {
	st := testutil.NewFakeStore()
	addDue(t, st, owner, "Call the dentist", ownerChat)
	n := &recordingNotifier{}

	for range 3 {
		if err := deliverDue(context.Background(), st, n, now); err != nil {
			t.Fatalf("deliverDue: %v", err)
		}
	}
	if len(n.sent) != 1 {
		t.Errorf("sent %d times, want once", len(n.sent))
	}
}

func TestTwoUsersDueAtOnceGetOnlyTheirOwn(t *testing.T) {
	st := testutil.NewFakeStore()
	addDue(t, st, owner, "owner's reminder", ownerChat)
	addDue(t, st, other, "other's reminder", otherChat)
	n := &recordingNotifier{}

	if err := deliverDue(context.Background(), st, n, now); err != nil {
		t.Fatalf("deliverDue: %v", err)
	}
	want := []notice{
		{chat: ownerChat, text: "Reminder: owner's reminder"},
		{chat: otherChat, text: "Reminder: other's reminder"},
	}
	if len(n.sent) != len(want) {
		t.Fatalf("sent %+v, want %+v", n.sent, want)
	}
	for i := range want {
		if n.sent[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, n.sent[i], want[i])
		}
	}
}
