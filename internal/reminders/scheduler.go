package reminders

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

// Notifier sends a message to a chat in one attempt. The Telegram channel implements it.
type Notifier interface {
	Notify(ctx context.Context, chatID int64, text string) error
}

// DueStore is the part of the store the scheduler uses.
type DueStore interface {
	UsersWithDueReminders(ctx context.Context, now time.Time) ([]identity.UserID, error)
	DueReminders(ctx context.Context, user identity.UserID, now time.Time) ([]store.Reminder, error)
	MarkReminderDelivered(ctx context.Context, user identity.UserID, id int64, at time.Time) (bool, error)
}

// Run delivers due reminders at startup and then every interval until ctx is done.
func Run(ctx context.Context, st DueStore, n Notifier, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if err := deliverDue(ctx, st, n, time.Now()); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "reminder delivery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// deliverDue sends every reminder that is due by now, each to the chat it was created in. A
// reminder is marked delivered only after its message was sent, so a failed send is tried again
// on the next tick. Each user's reminders are read with that user in context.
func deliverDue(ctx context.Context, st DueStore, n Notifier, now time.Time) error {
	users, err := st.UsersWithDueReminders(ctx, now)
	if err != nil {
		return fmt.Errorf("list users with due reminders: %w", err)
	}
	for _, user := range users {
		owner := identity.WithUser(ctx, user)
		due, err := st.DueReminders(owner, user, now)
		if err != nil {
			slog.ErrorContext(owner, "due reminders not read", "error", err)
			continue
		}
		for _, r := range due {
			if err := n.Notify(owner, r.ChatID, "Reminder: "+r.Text); err != nil {
				slog.WarnContext(owner, "reminder not sent; retried on next tick", "reminder_id", r.ID, "error", err)
				continue
			}
			if _, err := st.MarkReminderDelivered(owner, user, r.ID, now); err != nil {
				slog.ErrorContext(owner, "reminder sent but not recorded", "reminder_id", r.ID, "error", err)
			}
		}
	}
	return nil
}
