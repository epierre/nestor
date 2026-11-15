package testutil

import (
	"context"
	"slices"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

// The task and reminder rows of FakeStore. They live apart from the rest of the fake so that the
// two stay easy to compare with postgres/tasks.go.

// AddTask implements store.Store. An unparsable or impossible due day is refused, as the database
// refuses it.
func (s *FakeStore) AddTask(ctx context.Context, user identity.UserID, task store.Task) (store.Task, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Task{}, err
	}
	if len(task.Title) == 0 || len([]rune(task.Title)) > 200 {
		return store.Task{}, errBadTitle
	}
	if task.DueDate != "" {
		if _, err := time.Parse("2006-01-02", task.DueDate); err != nil {
			return store.Task{}, errBadDay
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	task.ID = s.id()
	task.UserID = user
	task.CreatedAt = time.Now()
	s.tasks = append(s.tasks, task)
	return task, nil
}

// ListTasks implements store.Store.
func (s *FakeStore) ListTasks(ctx context.Context, user identity.UserID, includeDone bool) ([]store.Task, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []store.Task
	for _, t := range s.tasks {
		if t.UserID == user && (includeDone || t.DoneAt == nil) {
			found = append(found, t)
		}
	}
	slices.SortStableFunc(found, func(a, b store.Task) int {
		if (a.DoneAt != nil) != (b.DoneAt != nil) {
			if a.DoneAt != nil {
				return 1
			}
			return -1
		}
		switch {
		case a.DueDate == b.DueDate:
			return int(a.ID - b.ID)
		case a.DueDate == "":
			return 1
		case b.DueDate == "":
			return -1
		case a.DueDate < b.DueDate:
			return -1
		}
		return 1
	})
	return found, nil
}

// GetTask implements store.Store.
func (s *FakeStore) GetTask(ctx context.Context, user identity.UserID, id int64) (store.Task, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Task{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.taskIndex(user, id)
	if i < 0 {
		return store.Task{}, store.ErrTaskNotFound
	}
	return s.tasks[i], nil
}

// CompleteTask implements store.Store.
func (s *FakeStore) CompleteTask(ctx context.Context, user identity.UserID, id int64, at time.Time) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.taskIndex(user, id)
	if i < 0 {
		return store.ErrTaskNotFound
	}
	if s.tasks[i].DoneAt == nil {
		s.tasks[i].DoneAt = &at
	}
	return nil
}

// DeleteTask implements store.Store.
func (s *FakeStore) DeleteTask(ctx context.Context, user identity.UserID, id int64) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.taskIndex(user, id)
	if i < 0 {
		return store.ErrTaskNotFound
	}
	s.tasks = slices.Delete(s.tasks, i, i+1)
	return nil
}

// AddReminder implements store.Store.
func (s *FakeStore) AddReminder(ctx context.Context, user identity.UserID, r store.Reminder) (store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Reminder{}, err
	}
	if len(r.Text) == 0 {
		return store.Reminder{}, errBadTitle
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = s.id()
	r.UserID = user
	r.CreatedAt = time.Now()
	s.reminders = append(s.reminders, r)
	return r, nil
}

// GetReminder implements store.Store.
func (s *FakeStore) GetReminder(ctx context.Context, user identity.UserID, id int64) (store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Reminder{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.reminderIndex(user, id)
	if i < 0 {
		return store.Reminder{}, store.ErrReminderNotFound
	}
	return s.reminders[i], nil
}

// ListReminders implements store.Store.
func (s *FakeStore) ListReminders(ctx context.Context, user identity.UserID) ([]store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	return s.pendingReminders(user, time.Time{}, false), nil
}

// DueReminders implements store.Store.
func (s *FakeStore) DueReminders(ctx context.Context, user identity.UserID, now time.Time) ([]store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	return s.pendingReminders(user, now, true), nil
}

// pendingReminders returns the user's pending reminders, earliest first, and when dueOnly is set,
// only those due by now.
func (s *FakeStore) pendingReminders(user identity.UserID, now time.Time, dueOnly bool) []store.Reminder {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []store.Reminder
	for _, r := range s.reminders {
		if r.UserID == user && r.DeliveredAt == nil && r.CancelledAt == nil && (!dueOnly || !r.DueAt.After(now)) {
			found = append(found, r)
		}
	}
	slices.SortStableFunc(found, func(a, b store.Reminder) int { return a.DueAt.Compare(b.DueAt) })
	return found
}

// CancelReminder implements store.Store.
func (s *FakeStore) CancelReminder(ctx context.Context, user identity.UserID, id int64, at time.Time) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.reminderIndex(user, id)
	if i < 0 || s.reminders[i].DeliveredAt != nil || s.reminders[i].CancelledAt != nil {
		return store.ErrReminderNotFound
	}
	s.reminders[i].CancelledAt = &at
	return nil
}

// MarkReminderDelivered implements store.Store.
func (s *FakeStore) MarkReminderDelivered(ctx context.Context, user identity.UserID, id int64, at time.Time) (bool, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.reminderIndex(user, id)
	if i < 0 || s.reminders[i].DeliveredAt != nil || s.reminders[i].CancelledAt != nil {
		return false, nil
	}
	s.reminders[i].DeliveredAt = &at
	return true, nil
}

// UsersWithDueReminders implements store.Store.
func (s *FakeStore) UsersWithDueReminders(_ context.Context, now time.Time) ([]identity.UserID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var users []identity.UserID
	for _, r := range s.reminders {
		if r.DeliveredAt == nil && r.CancelledAt == nil && !r.DueAt.After(now) && !slices.Contains(users, r.UserID) {
			users = append(users, r.UserID)
		}
	}
	return users, nil
}

func (s *FakeStore) taskIndex(user identity.UserID, id int64) int {
	for i, t := range s.tasks {
		if t.ID == id && t.UserID == user {
			return i
		}
	}
	return -1
}

func (s *FakeStore) reminderIndex(user identity.UserID, id int64) int {
	for i, r := range s.reminders {
		if r.ID == id && r.UserID == user {
			return i
		}
	}
	return -1
}
