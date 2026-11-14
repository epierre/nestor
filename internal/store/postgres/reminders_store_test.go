//go:build integration

package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

func addReminder(t *testing.T, st *Store, ctx context.Context, user identity.UserID, text string, due time.Time, chat int64) store.Reminder {
	t.Helper()
	r, err := st.AddReminder(ctx, user, store.Reminder{Text: text, DueAt: due, ChatID: chat})
	if err != nil {
		t.Fatalf("AddReminder: %v", err)
	}
	return r
}

func TestReminderRecordsItsChat(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	r := addReminder(t, st, ctxA, a, "Call the dentist", time.Now().Add(time.Hour), 555)

	got, err := st.GetReminder(ctxA, a, r.ID)
	if err != nil {
		t.Fatalf("GetReminder: %v", err)
	}
	if got.ChatID != 555 || got.Text != "Call the dentist" {
		t.Errorf("reminder %+v, want its text and chat 555", got)
	}
}

func TestDueRemindersPerUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	past := time.Now().Add(-time.Minute)
	ra := addReminder(t, st, ctxA, a, "A's", past, 501)
	rb := addReminder(t, st, ctxB, b, "B's", past, 502)
	now := time.Now()

	users, err := st.UsersWithDueReminders(context.Background(), now)
	if err != nil {
		t.Fatalf("UsersWithDueReminders: %v", err)
	}
	if !slices.Contains(users, a) || !slices.Contains(users, b) {
		t.Errorf("users with due reminders %v, want both %d and %d", users, a, b)
	}

	dueA, _ := st.DueReminders(ctxA, a, now)
	if len(dueA) != 1 || dueA[0].ID != ra.ID {
		t.Errorf("A's due reminders %+v, want only A's", dueA)
	}
	dueB, _ := st.DueReminders(ctxB, b, now)
	if len(dueB) != 1 || dueB[0].ID != rb.ID {
		t.Errorf("B's due reminders %+v, want only B's", dueB)
	}
}

func TestFutureReminderNotDue(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	addReminder(t, st, ctxA, a, "later", time.Now().Add(time.Hour), 501)

	if due, _ := st.DueReminders(ctxA, a, time.Now()); len(due) != 0 {
		t.Errorf("future reminder due now: %+v", due)
	}
}

func TestMarkDeliveredOnce(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	r := addReminder(t, st, ctxA, a, "once", time.Now().Add(-time.Minute), 501)

	if ok, err := st.MarkReminderDelivered(ctxA, a, r.ID, time.Now()); err != nil || !ok {
		t.Fatalf("first mark: ok=%v err=%v, want true", ok, err)
	}
	if ok, _ := st.MarkReminderDelivered(ctxA, a, r.ID, time.Now()); ok {
		t.Error("second mark reported a change")
	}
	if due, _ := st.DueReminders(ctxA, a, time.Now()); len(due) != 0 {
		t.Errorf("delivered reminder still due: %+v", due)
	}
}

func TestCancelledReminderNeverDue(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	r := addReminder(t, st, ctxA, a, "dropped", time.Now().Add(-time.Minute), 501)
	if err := st.CancelReminder(ctxA, a, r.ID, time.Now()); err != nil {
		t.Fatalf("CancelReminder: %v", err)
	}

	if due, _ := st.DueReminders(ctxA, a, time.Now()); len(due) != 0 {
		t.Errorf("cancelled reminder due: %+v", due)
	}
	if err := st.CancelReminder(ctxA, a, r.ID, time.Now()); !errors.Is(err, store.ErrReminderNotFound) {
		t.Errorf("cancelling twice: err = %v, want ErrReminderNotFound", err)
	}
}

func TestReminderScopedToUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	r := addReminder(t, st, ctxA, a, "private", time.Now().Add(time.Hour), 501)

	if _, err := st.GetReminder(ctxB, b, r.ID); !errors.Is(err, store.ErrReminderNotFound) {
		t.Errorf("other user GetReminder: err = %v, want ErrReminderNotFound", err)
	}
	if err := st.CancelReminder(ctxB, b, r.ID, time.Now()); !errors.Is(err, store.ErrReminderNotFound) {
		t.Errorf("other user CancelReminder: err = %v, want ErrReminderNotFound", err)
	}
	if list, _ := st.ListReminders(ctxB, b); len(list) != 0 {
		t.Errorf("other user lists %+v, want none", list)
	}
}
