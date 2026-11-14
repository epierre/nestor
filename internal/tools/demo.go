package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/epierre/nestor/internal/identity"
)

// CurrentTimeTool returns the current time.
type CurrentTimeTool struct {
	now func() time.Time
}

// NewCurrentTimeTool returns a new CurrentTimeTool using the system clock.
func NewCurrentTimeTool() *CurrentTimeTool {
	return NewCurrentTimeToolWithClock(time.Now)
}

// NewCurrentTimeToolWithClock returns a new CurrentTimeTool with a custom clock function.
func NewCurrentTimeToolWithClock(now func() time.Time) *CurrentTimeTool {
	return &CurrentTimeTool{now: now}
}

func (t *CurrentTimeTool) Name() string {
	return "current_time"
}

func (t *CurrentTimeTool) Description() string {
	return "Returns the current date and time in UTC or a specified timezone."
}

func (t *CurrentTimeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"zone": {
				"type": "string",
				"description": "Optional IANA time zone name (e.g. 'UTC', 'America/New_York')"
			}
		},
		"additionalProperties": false
	}`)
}

func (t *CurrentTimeTool) Risk() Risk {
	return RiskRead
}

func (t *CurrentTimeTool) Preview(args json.RawMessage) (string, error) {
	var a struct {
		Zone string `json:"zone"`
	}
	if len(bytes.TrimSpace(args)) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	if a.Zone != "" {
		return fmt.Sprintf("Check current time in %s", a.Zone), nil
	}
	return "Check current time", nil
}

func (t *CurrentTimeTool) Execute(_ context.Context, args json.RawMessage) (Result, error) {
	var a struct {
		Zone string `json:"zone"`
	}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrValidation, err)
		}
	}

	loc := time.UTC
	if a.Zone != "" {
		loadedLoc, err := time.LoadLocation(a.Zone)
		if err != nil {
			return Result{}, fmt.Errorf("invalid time zone %q: %w", a.Zone, err)
		}
		loc = loadedLoc
	}

	now := t.now().In(loc)
	return Result{
		Content: now.Format(time.RFC3339),
	}, nil
}

// MessageSender sends a message to a user.
type MessageSender func(ctx context.Context, userID identity.UserID, text string) error

// NotifySelfTool sends a notification message to the requesting user.
type NotifySelfTool struct {
	sender MessageSender
}

// NewNotifySelfTool returns a new NotifySelfTool.
func NewNotifySelfTool(sender MessageSender) *NotifySelfTool {
	return &NotifySelfTool{sender: sender}
}

func (t *NotifySelfTool) Name() string {
	return "notify_self"
}

func (t *NotifySelfTool) Description() string {
	return "Sends a notification message to the user's chat."
}

func (t *NotifySelfTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"text": {
				"type": "string",
				"description": "The message text to send"
			}
		},
		"required": ["text"],
		"additionalProperties": false
	}`)
}

func (t *NotifySelfTool) Risk() Risk {
	return RiskCritical
}

func (t *NotifySelfTool) Preview(args json.RawMessage) (string, error) {
	var a struct {
		Text string `json:"text"`
	}
	if len(bytes.TrimSpace(args)) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	return fmt.Sprintf("Notify yourself: %s", a.Text), nil
}

func (t *NotifySelfTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	userID, err := identity.UserFrom(ctx)
	if err != nil {
		return Result{}, err
	}

	var a struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if strings.TrimSpace(a.Text) == "" {
		return Result{}, fmt.Errorf("%w: text cannot be empty", ErrValidation)
	}

	if t.sender != nil {
		if err := t.sender(ctx, userID, a.Text); err != nil {
			return Result{}, fmt.Errorf("send notification: %w", err)
		}
	}

	return Result{
		Content: fmt.Sprintf("Notification sent: %s", a.Text),
	}, nil
}

// RegisterDemoTools registers current_time and notify_self in r if enabled is true.
func RegisterDemoTools(r *Registry, enabled bool, sender MessageSender) error {
	if !enabled {
		return nil
	}
	if err := r.Register(NewCurrentTimeTool()); err != nil {
		return err
	}
	if err := r.Register(NewNotifySelfTool(sender)); err != nil {
		return err
	}
	return nil
}
