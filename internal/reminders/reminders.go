// Package reminders lets the owner ask to be reminded of something at a time, and delivers each
// reminder when it is due. Every tool acts only for the user in the request context.
package reminders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/tools"
)

const maxTextLength = 500

// Store is the part of the store the reminder tools use.
type Store interface {
	AddReminder(ctx context.Context, user identity.UserID, r store.Reminder) (store.Reminder, error)
	GetReminder(ctx context.Context, user identity.UserID, id int64) (store.Reminder, error)
	ListReminders(ctx context.Context, user identity.UserID) ([]store.Reminder, error)
	CancelReminder(ctx context.Context, user identity.UserID, id int64, at time.Time) error
}

// Tools returns the reminder tools bound to st. Times the model gives are read in loc, and now
// tells which of them have already passed.
func Tools(st Store, loc *time.Location, now func() time.Time) []tools.Tool {
	return []tools.Tool{
		&addTool{st: st, loc: loc, now: now},
		&listTool{st: st, loc: loc},
		&cancelTool{st: st, loc: loc, now: now},
	}
}

// parseDue reads a due time. It accepts RFC 3339 with an offset, a local date and time, or a clock
// time meaning today, the last two in loc. A time that is not in the future is refused.
func parseDue(s string, loc *time.Location, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	due, err := time.Parse(time.RFC3339, s)
	if err != nil {
		due, err = parseLocal(s, loc, now)
	}
	if err != nil {
		return time.Time{}, errors.New("due must be a time such as 15:00 or 2026-11-15 08:30")
	}
	if !due.After(now) {
		return time.Time{}, errors.New("that time has already passed")
	}
	return due, nil
}

func parseLocal(s string, loc *time.Location, now time.Time) (time.Time, error) {
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	clock, err := time.ParseInLocation("15:04", s, loc)
	if err != nil {
		return time.Time{}, err
	}
	today := now.In(loc)
	return time.Date(today.Year(), today.Month(), today.Day(), clock.Hour(), clock.Minute(), 0, 0, loc), nil
}

// show formats a due instant in the owner's zone.
func show(due time.Time, loc *time.Location) string {
	return due.In(loc).Format("2006-01-02 15:04")
}

type addArgs struct {
	Text string `json:"text"`
	Due  string `json:"due"`
}

type addTool struct {
	st  Store
	loc *time.Location
	now func() time.Time
}

func (t *addTool) Name() string { return "reminder_add" }
func (t *addTool) Description() string {
	return "Sets a reminder that is sent to this chat at the given time. The time is a clock time such as 15:00 or a local date and time such as 2026-11-15 08:30."
}
func (t *addTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {"type": "string", "minLength": 1, "maxLength": 500},
			"due": {"type": "string", "minLength": 1}
		},
		"required": ["text", "due"],
		"additionalProperties": false
	}`)
}
func (t *addTool) Risk() tools.Risk { return tools.RiskReversible }
func (t *addTool) Preview(args json.RawMessage) (string, error) {
	var a addArgs
	_ = json.Unmarshal(args, &a)
	return "Set reminder: " + a.Text, nil
}

func (t *addTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	var a addArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	text := strings.TrimSpace(a.Text)
	if n := utf8.RuneCountInString(text); n < 1 || n > maxTextLength {
		return tools.Result{}, errors.New("text must be 1 to 500 characters")
	}
	chatID, ok := channel.ChatFrom(ctx)
	if !ok {
		return tools.Result{}, errors.New("no chat to send the reminder to")
	}
	now := t.now()
	due, err := parseDue(a.Due, t.loc, now)
	if err != nil {
		return tools.Result{}, err
	}
	added, err := t.st.AddReminder(ctx, user, store.Reminder{Text: text, DueAt: due, ChatID: chatID})
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("Reminder #%d set for %s: %s", added.ID, show(added.DueAt, t.loc), text)}, nil
}

type listTool struct {
	st  Store
	loc *time.Location
}

func (t *listTool) Name() string { return "reminder_list" }
func (t *listTool) Description() string {
	return "Lists the user's pending reminders with their IDs and times."
}
func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type": "object", "additionalProperties": false}`)
}
func (t *listTool) Risk() tools.Risk { return tools.RiskRead }
func (t *listTool) Preview(json.RawMessage) (string, error) {
	return "List reminders", nil
}

func (t *listTool) Execute(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	list, err := t.st.ListReminders(ctx, user)
	if err != nil {
		return tools.Result{}, err
	}
	if len(list) == 0 {
		return tools.Result{Content: "No reminders."}, nil
	}
	lines := make([]string, 0, len(list))
	for _, r := range list {
		lines = append(lines, fmt.Sprintf("#%d %s: %s", r.ID, show(r.DueAt, t.loc), r.Text))
	}
	return tools.Result{Content: strings.Join(lines, "\n")}, nil
}

type idArgs struct {
	ID int64 `json:"id"`
}

// cancelTool is critical: cancelling removes data, so Execute runs only from the approval gate.
type cancelTool struct {
	st  Store
	loc *time.Location
	now func() time.Time
}

func (t *cancelTool) Name() string { return "reminder_cancel" }
func (t *cancelTool) Description() string {
	return "Cancels one of the user's pending reminders, by ID. The owner must approve the cancellation first."
}
func (t *cancelTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"id": {"type": "integer", "minimum": 1}},
		"required": ["id"],
		"additionalProperties": false
	}`)
}
func (t *cancelTool) Risk() tools.Risk { return tools.RiskCritical }
func (t *cancelTool) Preview(args json.RawMessage) (string, error) {
	var a idArgs
	_ = json.Unmarshal(args, &a)
	return fmt.Sprintf("Cancel reminder #%d", a.ID), nil
}

// PreviewFor names the stored reminder, so the owner sees what will be cancelled.
func (t *cancelTool) PreviewFor(ctx context.Context, args json.RawMessage) (string, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return "", err
	}
	var a idArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	r, err := t.st.GetReminder(ctx, user, a.ID)
	if errors.Is(err, store.ErrReminderNotFound) {
		return fmt.Sprintf("Cancel reminder #%d (no such reminder)", a.ID), nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Cancel reminder: %s (%s)", r.Text, show(r.DueAt, t.loc)), nil
}

func (t *cancelTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	var a idArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	r, err := t.st.GetReminder(ctx, user, a.ID)
	if err != nil {
		return tools.Result{}, notPending(err, a.ID)
	}
	if err := t.st.CancelReminder(ctx, user, a.ID, t.now()); err != nil {
		return tools.Result{}, notPending(err, a.ID)
	}
	return tools.Result{Content: fmt.Sprintf("Cancelled reminder #%d: %s", a.ID, r.Text)}, nil
}

// notPending makes an unknown, foreign, or no longer pending reminder read the same way.
func notPending(err error, id int64) error {
	if errors.Is(err, store.ErrReminderNotFound) {
		return fmt.Errorf("no pending reminder #%d: %w", id, store.ErrReminderNotFound)
	}
	return err
}
