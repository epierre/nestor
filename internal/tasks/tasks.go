// Package tasks gives the model tools to keep the owner's task list. Every tool acts only for the
// user in the request context.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/tools"
)

const (
	maxTitleLength = 200
	dayLayout      = "2006-01-02"
)

// Store is the part of the store the task tools use.
type Store interface {
	AddTask(ctx context.Context, user identity.UserID, task store.Task) (store.Task, error)
	ListTasks(ctx context.Context, user identity.UserID, includeDone bool) ([]store.Task, error)
	GetTask(ctx context.Context, user identity.UserID, id int64) (store.Task, error)
	CompleteTask(ctx context.Context, user identity.UserID, id int64, at time.Time) error
	DeleteTask(ctx context.Context, user identity.UserID, id int64) error
}

// Tools returns the task tools bound to st.
func Tools(st Store) []tools.Tool {
	return []tools.Tool{
		&addTool{st: st},
		&listTool{st: st},
		&completeTool{st: st},
		&deleteTool{st: st},
	}
}

func userFrom(ctx context.Context) (identity.UserID, error) {
	return identity.UserFrom(ctx)
}

// decode reads arguments into v, refusing anything the schema does not allow.
func decode(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	return json.Unmarshal(args, v)
}

type addArgs struct {
	Title string `json:"title"`
	Due   string `json:"due"`
}

type addTool struct{ st Store }

func (t *addTool) Name() string { return "task_add" }
func (t *addTool) Description() string {
	return "Adds a task for the user, optionally due on a day (YYYY-MM-DD)."
}
func (t *addTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {"type": "string", "minLength": 1, "maxLength": 200},
			"due": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"}
		},
		"required": ["title"],
		"additionalProperties": false
	}`)
}
func (t *addTool) Risk() tools.Risk { return tools.RiskReversible }
func (t *addTool) Preview(args json.RawMessage) (string, error) {
	var a addArgs
	_ = decode(args, &a)
	return "Add task: " + a.Title, nil
}

func (t *addTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := userFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	var a addArgs
	if err := decode(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	title := strings.TrimSpace(a.Title)
	if n := utf8.RuneCountInString(title); n < 1 || n > maxTitleLength {
		return tools.Result{}, errors.New("title must be 1 to 200 characters")
	}
	if a.Due != "" {
		if _, err := time.Parse(dayLayout, a.Due); err != nil {
			return tools.Result{}, errors.New("due must be a real day in YYYY-MM-DD form")
		}
	}
	added, err := t.st.AddTask(ctx, user, store.Task{Title: title, DueDate: a.Due})
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("Added task #%d: %s", added.ID, describe(added))}, nil
}

type listTool struct{ st Store }

type listArgs struct {
	IncludeDone bool `json:"include_done"`
}

func (t *listTool) Name() string { return "task_list" }
func (t *listTool) Description() string {
	return "Lists the user's open tasks with their IDs and due days, earliest due first."
}
func (t *listTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {"include_done": {"type": "boolean"}},
		"additionalProperties": false
	}`)
}
func (t *listTool) Risk() tools.Risk { return tools.RiskRead }
func (t *listTool) Preview(json.RawMessage) (string, error) {
	return "List tasks", nil
}

func (t *listTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := userFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	var a listArgs
	if err := decode(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	list, err := t.st.ListTasks(ctx, user, a.IncludeDone)
	if err != nil {
		return tools.Result{}, err
	}
	if len(list) == 0 {
		return tools.Result{Content: "No tasks."}, nil
	}
	lines := make([]string, 0, len(list))
	for _, task := range list {
		lines = append(lines, fmt.Sprintf("#%d %s", task.ID, describe(task)))
	}
	return tools.Result{Content: strings.Join(lines, "\n")}, nil
}

type idArgs struct {
	ID int64 `json:"id"`
}

type completeTool struct{ st Store }

func (t *completeTool) Name() string { return "task_complete" }
func (t *completeTool) Description() string {
	return "Marks one of the user's tasks, by ID, as done."
}
func (t *completeTool) Schema() json.RawMessage { return idSchema }
func (t *completeTool) Risk() tools.Risk        { return tools.RiskReversible }
func (t *completeTool) Preview(args json.RawMessage) (string, error) {
	var a idArgs
	_ = decode(args, &a)
	return fmt.Sprintf("Complete task #%d", a.ID), nil
}

func (t *completeTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := userFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	var a idArgs
	if err := decode(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	if err := t.st.CompleteTask(ctx, user, a.ID, time.Now()); err != nil {
		return tools.Result{}, notFound(err, a.ID)
	}
	return tools.Result{Content: fmt.Sprintf("Completed task #%d.", a.ID)}, nil
}

// deleteTool is critical: deleting data needs the owner's approval, so Execute runs only from the
// approval gate.
type deleteTool struct{ st Store }

func (t *deleteTool) Name() string { return "task_delete" }
func (t *deleteTool) Description() string {
	return "Deletes one of the user's tasks, by ID. The owner must approve the deletion first."
}
func (t *deleteTool) Schema() json.RawMessage { return idSchema }
func (t *deleteTool) Risk() tools.Risk        { return tools.RiskCritical }
func (t *deleteTool) Preview(args json.RawMessage) (string, error) {
	var a idArgs
	_ = decode(args, &a)
	return fmt.Sprintf("Delete task #%d", a.ID), nil
}

// PreviewFor names the stored task, so that the owner sees what will be deleted. An ID that is not
// the owner's still gets a card; approving it then fails as an unknown task.
func (t *deleteTool) PreviewFor(ctx context.Context, args json.RawMessage) (string, error) {
	user, err := userFrom(ctx)
	if err != nil {
		return "", err
	}
	var a idArgs
	if err := decode(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	task, err := t.st.GetTask(ctx, user, a.ID)
	if errors.Is(err, store.ErrTaskNotFound) {
		return fmt.Sprintf("Delete task #%d (no such task)", a.ID), nil
	}
	if err != nil {
		return "", err
	}
	return "Delete task: " + describe(task), nil
}

func (t *deleteTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := userFrom(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	var a idArgs
	if err := decode(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("arguments: %w", err)
	}
	task, err := t.st.GetTask(ctx, user, a.ID)
	if err != nil {
		return tools.Result{}, notFound(err, a.ID)
	}
	if err := t.st.DeleteTask(ctx, user, a.ID); err != nil {
		return tools.Result{}, notFound(err, a.ID)
	}
	return tools.Result{Content: "Deleted task: " + task.Title}, nil
}

var idSchema = json.RawMessage(`{
	"type": "object",
	"properties": {"id": {"type": "integer", "minimum": 1}},
	"required": ["id"],
	"additionalProperties": false
}`)

// notFound makes an unknown or foreign task ID read the same either way.
func notFound(err error, id int64) error {
	if errors.Is(err, store.ErrTaskNotFound) {
		return fmt.Errorf("no task #%d: %w", id, store.ErrTaskNotFound)
	}
	return err
}

// describe is a task's title with its due day and state, for the model and the owner.
func describe(task store.Task) string {
	text := task.Title
	if task.DueDate != "" {
		text += " (due " + task.DueDate + ")"
	}
	if task.DoneAt != nil {
		text += " [done]"
	}
	return text
}
