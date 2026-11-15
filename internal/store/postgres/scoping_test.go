package postgres

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/testutil"
)

// A Store without a pool panics on any query, so an error here proves none was attempted. Every
// method on store.Store other than ListActiveUsers (which reads only the identity directory) must
// appear in this table and reject unscoped or mismatched contexts on both Store implementations.
func TestUnscopedCallReturnsError(t *testing.T) {
	const user identity.UserID = 1
	now := time.Now()
	stores := []struct {
		name string
		st   store.Store
	}{
		{"postgres", New(nil)},
		{"fake", testutil.NewFakeStore()},
	}

	methodsFor := func(st store.Store) []struct {
		name string
		call func(ctx context.Context) error
	} {
		return []struct {
			name string
			call func(ctx context.Context) error
		}{
			{"ActiveConversation", func(ctx context.Context) error {
				_, err := st.ActiveConversation(ctx, user)
				return err
			}},
			{"CloseConversation", func(ctx context.Context) error {
				return st.CloseConversation(ctx, user)
			}},
			{"AppendMessage", func(ctx context.Context) error {
				_, err := st.AppendMessage(ctx, user, 1, model.RoleUser, "hello")
				return err
			}},
			{"RecentMessages", func(ctx context.Context) error {
				_, err := st.RecentMessages(ctx, user, 1, 10)
				return err
			}},
			{"RecordModelCall", func(ctx context.Context) error {
				return st.RecordModelCall(ctx, user, store.ModelCall{})
			}},
			{"RecordToolCall", func(ctx context.Context) error {
				return st.RecordToolCall(ctx, user, store.ToolCall{})
			}},
			{"ProposeAction", func(ctx context.Context) error {
				return st.ProposeAction(ctx, user, store.PendingAction{})
			}},
			{"GetPendingAction", func(ctx context.Context) error {
				_, err := st.GetPendingAction(ctx, user, "abc")
				return err
			}},
			{"UpdatePendingAction", func(ctx context.Context) error {
				_, err := st.UpdatePendingAction(ctx, user, "abc", store.PendingProposed, store.PendingUpdate{})
				return err
			}},
			{"FailStaleApprovals", func(ctx context.Context) error {
				_, err := st.FailStaleApprovals(ctx, user, now)
				return err
			}},
			{"UnnotifiedOutcomes", func(ctx context.Context) error {
				_, err := st.UnnotifiedOutcomes(ctx, user)
				return err
			}},
			{"MarkOutcomeNoticed", func(ctx context.Context) error {
				return st.MarkOutcomeNoticed(ctx, user, "abc", now)
			}},
			{"SupersedePending", func(ctx context.Context) error {
				_, err := st.SupersedePending(ctx, user, "notify_self", "abc", "def")
				return err
			}},
			{"ProposedActions", func(ctx context.Context) error {
				_, err := st.ProposedActions(ctx, user, now)
				return err
			}},
			{"MarkCardSent", func(ctx context.Context) error {
				return st.MarkCardSent(ctx, user, "abc", 100, now)
			}},
			{"UnsentCards", func(ctx context.Context) error {
				_, err := st.UnsentCards(ctx, user, now)
				return err
			}},
			{"AddTask", func(ctx context.Context) error {
				_, err := st.AddTask(ctx, user, store.Task{Title: "Renew passport"})
				return err
			}},
			{"ListTasks", func(ctx context.Context) error {
				_, err := st.ListTasks(ctx, user, false)
				return err
			}},
			{"GetTask", func(ctx context.Context) error {
				_, err := st.GetTask(ctx, user, 1)
				return err
			}},
			{"CompleteTask", func(ctx context.Context) error {
				return st.CompleteTask(ctx, user, 1, now)
			}},
			{"DeleteTask", func(ctx context.Context) error {
				return st.DeleteTask(ctx, user, 1)
			}},
			{"AddReminder", func(ctx context.Context) error {
				_, err := st.AddReminder(ctx, user, store.Reminder{Text: "Call", DueAt: now})
				return err
			}},
			{"GetReminder", func(ctx context.Context) error {
				_, err := st.GetReminder(ctx, user, 1)
				return err
			}},
			{"ListReminders", func(ctx context.Context) error {
				_, err := st.ListReminders(ctx, user)
				return err
			}},
			{"DueReminders", func(ctx context.Context) error {
				_, err := st.DueReminders(ctx, user, now)
				return err
			}},
			{"CancelReminder", func(ctx context.Context) error {
				return st.CancelReminder(ctx, user, 1, now)
			}},
			{"MarkReminderDelivered", func(ctx context.Context) error {
				_, err := st.MarkReminderDelivered(ctx, user, 1, now)
				return err
			}},
		}
	}

	covered := map[string]bool{"ListActiveUsers": true}
	for _, m := range methodsFor(New(nil)) {
		covered[m.name] = true
	}
	iface := reflect.TypeFor[store.Store]()
	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if !covered[name] {
			t.Errorf("store.Store method %s is missing from TestUnscopedCallReturnsError", name)
		}
	}

	contexts := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"no user", context.Background(), identity.ErrNoUser},
		{"another user", identity.WithUser(context.Background(), 2), store.ErrUserMismatch},
	}
	for _, impl := range stores {
		for _, m := range methodsFor(impl.st) {
			for _, c := range contexts {
				t.Run(impl.name+"/"+m.name+"/"+c.name, func(t *testing.T) {
					if err := m.call(c.ctx); !errors.Is(err, c.want) {
						t.Errorf("got error %v, want %v", err, c.want)
					}
				})
			}
		}
	}
}
