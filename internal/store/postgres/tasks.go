package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

// taskColumns lists the columns that scanTask reads, in its order. The due day is returned as text
// so that no time zone is attached to it.
const taskColumns = `id, user_id, title, COALESCE(to_char(due_date, 'YYYY-MM-DD'), ''), created_at, done_at`

func scanTask(row pgx.Row) (store.Task, error) {
	var task store.Task
	err := row.Scan(&task.ID, &task.UserID, &task.Title, &task.DueDate, &task.CreatedAt, &task.DoneAt)
	return task, err
}

// AddTask implements store.Store. An impossible due day or a title outside 1 to 200 characters is
// refused by the database.
func (s *Store) AddTask(ctx context.Context, user identity.UserID, task store.Task) (store.Task, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Task{}, err
	}
	added, err := scanTask(s.pool.QueryRow(ctx, `
		INSERT INTO tasks (user_id, title, due_date)
		VALUES ($1, $2, NULLIF($3, '')::date)
		RETURNING `+taskColumns, user, task.Title, task.DueDate))
	if err != nil {
		return store.Task{}, fmt.Errorf("add task: %w", err)
	}
	return added, nil
}

// ListTasks implements store.Store.
func (s *Store) ListTasks(ctx context.Context, user identity.UserID, includeDone bool) ([]store.Task, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+taskColumns+`
		FROM tasks
		WHERE user_id = $1 AND ($2 OR done_at IS NULL)
		ORDER BY done_at IS NOT NULL, due_date ASC NULLS LAST, id`, user, includeDone)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	var found []store.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		found = append(found, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return found, nil
}

// GetTask implements store.Store.
func (s *Store) GetTask(ctx context.Context, user identity.UserID, id int64) (store.Task, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Task{}, err
	}
	task, err := scanTask(s.pool.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM tasks WHERE id = $1 AND user_id = $2`, id, user))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Task{}, store.ErrTaskNotFound
	}
	if err != nil {
		return store.Task{}, fmt.Errorf("get task: %w", err)
	}
	return task, nil
}

// CompleteTask implements store.Store.
func (s *Store) CompleteTask(ctx context.Context, user identity.UserID, id int64, at time.Time) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE tasks SET done_at = COALESCE(done_at, $3)
		WHERE id = $1 AND user_id = $2`, id, user, at)
	if err != nil {
		return fmt.Errorf("complete task: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return store.ErrTaskNotFound
	}
	return nil
}

// DeleteTask implements store.Store.
func (s *Store) DeleteTask(ctx context.Context, user identity.UserID, id int64) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM tasks WHERE id = $1 AND user_id = $2`, id, user)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return store.ErrTaskNotFound
	}
	return nil
}

// reminderColumns lists the columns that scanReminder reads, in its order.
const reminderColumns = `id, user_id, text, due_at, chat_id, created_at, delivered_at, cancelled_at`

func scanReminder(row pgx.Row) (store.Reminder, error) {
	var r store.Reminder
	err := row.Scan(&r.ID, &r.UserID, &r.Text, &r.DueAt, &r.ChatID, &r.CreatedAt, &r.DeliveredAt, &r.CancelledAt)
	return r, err
}

// AddReminder implements store.Store.
func (s *Store) AddReminder(ctx context.Context, user identity.UserID, r store.Reminder) (store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Reminder{}, err
	}
	added, err := scanReminder(s.pool.QueryRow(ctx, `
		INSERT INTO reminders (user_id, text, due_at, chat_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+reminderColumns, user, r.Text, r.DueAt, r.ChatID))
	if err != nil {
		return store.Reminder{}, fmt.Errorf("add reminder: %w", err)
	}
	return added, nil
}

// GetReminder implements store.Store.
func (s *Store) GetReminder(ctx context.Context, user identity.UserID, id int64) (store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Reminder{}, err
	}
	r, err := scanReminder(s.pool.QueryRow(ctx, `
		SELECT `+reminderColumns+`
		FROM reminders WHERE id = $1 AND user_id = $2`, id, user))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Reminder{}, store.ErrReminderNotFound
	}
	if err != nil {
		return store.Reminder{}, fmt.Errorf("get reminder: %w", err)
	}
	return r, nil
}

// ListReminders implements store.Store.
func (s *Store) ListReminders(ctx context.Context, user identity.UserID) ([]store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	return s.queryReminders(ctx, `
		SELECT `+reminderColumns+`
		FROM reminders
		WHERE user_id = $1 AND delivered_at IS NULL AND cancelled_at IS NULL
		ORDER BY due_at, id`, user)
}

// DueReminders implements store.Store.
func (s *Store) DueReminders(ctx context.Context, user identity.UserID, now time.Time) ([]store.Reminder, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	return s.queryReminders(ctx, `
		SELECT `+reminderColumns+`
		FROM reminders
		WHERE user_id = $1 AND delivered_at IS NULL AND cancelled_at IS NULL AND due_at <= $2
		ORDER BY due_at, id`, user, now)
}

func (s *Store) queryReminders(ctx context.Context, sql string, args ...any) ([]store.Reminder, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list reminders: %w", err)
	}
	defer rows.Close()
	var found []store.Reminder
	for rows.Next() {
		r, err := scanReminder(rows)
		if err != nil {
			return nil, fmt.Errorf("scan reminder: %w", err)
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list reminders: %w", err)
	}
	return found, nil
}

// CancelReminder implements store.Store.
func (s *Store) CancelReminder(ctx context.Context, user identity.UserID, id int64, at time.Time) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE reminders SET cancelled_at = $3
		WHERE id = $1 AND user_id = $2 AND delivered_at IS NULL AND cancelled_at IS NULL`, id, user, at)
	if err != nil {
		return fmt.Errorf("cancel reminder: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return store.ErrReminderNotFound
	}
	return nil
}

// MarkReminderDelivered implements store.Store.
func (s *Store) MarkReminderDelivered(ctx context.Context, user identity.UserID, id int64, at time.Time) (bool, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE reminders SET delivered_at = $3
		WHERE id = $1 AND user_id = $2 AND delivered_at IS NULL AND cancelled_at IS NULL`, id, user, at)
	if err != nil {
		return false, fmt.Errorf("mark reminder delivered: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// UsersWithDueReminders implements store.Store. It is deliberately unscoped: see the interface.
func (s *Store) UsersWithDueReminders(ctx context.Context, now time.Time) ([]identity.UserID, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT user_id FROM reminders
		WHERE delivered_at IS NULL AND cancelled_at IS NULL AND due_at <= $1`, now)
	if err != nil {
		return nil, fmt.Errorf("list users with due reminders: %w", err)
	}
	defer rows.Close()
	var users []identity.UserID
	for rows.Next() {
		var id identity.UserID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users with due reminders: %w", err)
	}
	return users, nil
}
