// Package store defines persistence for user data. Every method is scoped to one user.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/tools"
)

var (
	// ErrUserMismatch is returned when a store call names a different user than its context.
	ErrUserMismatch = errors.New("user ID does not match context")
	// ErrConversationNotFound is returned when a conversation does not exist or belongs to
	// another user. The two are not told apart.
	ErrConversationNotFound = errors.New("conversation not found")
	// ErrIdentityNotFound is returned when a platform identity is unknown or disabled. The two
	// are not told apart.
	ErrIdentityNotFound = errors.New("identity not found")
)

// Conversation is a sequence of messages owned by one user.
type Conversation struct {
	ID        int64
	UserID    identity.UserID
	StartedAt time.Time
	EndedAt   *time.Time // nil while the conversation is open
}

// Message is one stored message of a conversation.
type Message struct {
	ID             int64
	UserID         identity.UserID
	ConversationID int64
	Role           model.Role
	Content        string
	CreatedAt      time.Time
}

// Values of ModelCall.Status.
const (
	CallStatusOK    = "ok"
	CallStatusError = "error"
)

// ModelCall is one row of the call log.
type ModelCall struct {
	ID               int64
	UserID           identity.UserID
	ConversationID   int64
	Model            string
	StartedAt        time.Time
	Latency          time.Duration
	PromptTokens     int
	CompletionTokens int
	CostUSD          *float64 // nil, not zero, when the proxy reports no cost
	Status           string
	Error            string
	RequestJSON      json.RawMessage
	ResponseJSON     json.RawMessage
}

// Values of ToolCall.Status.
const (
	ToolCallStatusExecuted = "executed"
	ToolCallStatusRejected = "rejected"
	ToolCallStatusError    = "error"
	ToolCallStatusPending  = "pending"
	ToolCallStatusProposed = "proposed"
)

// ToolCall is one row of the tool call log.
type ToolCall struct {
	ID             int64
	UserID         identity.UserID
	ConversationID int64
	ModelCallID    *int64
	ToolName       string
	ArgsJSON       json.RawMessage
	Risk           tools.Risk
	Status         string
	ResultJSON     json.RawMessage
	Error          string
	StartedAt      time.Time
	Latency        time.Duration
}

// Values of PendingAction.State. Only the proposed state is written so far; the others are
// listed so the stored states match the design.
const (
	PendingProposed   = "proposed"
	PendingApproved   = "approved"
	PendingRejected   = "rejected"
	PendingExecuted   = "executed"
	PendingFailed     = "failed"
	PendingExpired    = "expired"
	PendingSuperseded = "superseded"
)

// OutcomeUnknownError is the error of an approved action that was never recorded as executed. Such
// an action may or may not have run, so it is failed and never retried.
const OutcomeUnknownError = "outcome unknown: may or may not have run; not retried"

// ErrActionNotFound is returned when a pending action does not exist or belongs to another user.
// The two are not told apart.
var ErrActionNotFound = errors.New("pending action not found")

// PendingAction is a critical tool call waiting for its user's approval. ArgsJSON is the exact
// text that ArgsHash covers.
type PendingAction struct {
	ID           string
	UserID       identity.UserID
	ToolName     string
	ArgsJSON     json.RawMessage
	ArgsHash     string
	Preview      string
	State        string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	DecidedAt    *time.Time // nil until the user or the clock decides
	ExecutedAt   *time.Time // nil until the tool has run, or failed to
	ResultJSON   json.RawMessage
	Error        string
	SupersededBy string     // set when a replacement for this action was proposed
	CardSentAt   *time.Time // nil until its approval card was delivered
	// NotifyChatID is the chat the card went to, where notices for this action are sent. 0 when
	// none was recorded.
	NotifyChatID        int64
	OutcomeNoticeSentAt *time.Time // nil until the outcome-unknown notice was delivered
}

// FailedApproval is an approved action that the sweep marked failed as outcome unknown.
type FailedApproval struct {
	ID      string
	UserID  identity.UserID
	Preview string
	ChatID  int64 // 0 when no card chat was recorded
}

// PendingUpdate sets the fields that a decision changes. A nil time leaves the stored one alone.
type PendingUpdate struct {
	State      string
	DecidedAt  *time.Time
	ExecutedAt *time.Time
	ResultJSON json.RawMessage
	Error      string
}

// ErrTaskNotFound is returned when a task does not exist or belongs to another user. The two are
// not told apart.
var ErrTaskNotFound = errors.New("task not found")

// ErrReminderNotFound is returned when a reminder does not exist, belongs to another user, or is
// no longer pending (delivered or cancelled). These are not told apart.
var ErrReminderNotFound = errors.New("reminder not found")

// Task is a task the owner keeps. DueDate is a calendar day, YYYY-MM-DD, or empty for none.
type Task struct {
	ID        int64
	UserID    identity.UserID
	Title     string
	DueDate   string
	CreatedAt time.Time
	DoneAt    *time.Time // nil while the task is open
}

// Reminder asks for Text to be sent to ChatID at DueAt.
type Reminder struct {
	ID          int64
	UserID      identity.UserID
	Text        string
	DueAt       time.Time
	ChatID      int64
	CreatedAt   time.Time
	DeliveredAt *time.Time // nil until sent
	CancelledAt *time.Time // nil unless cancelled
}

// Store persists conversations, messages and model calls. Every method takes the user ID
// explicitly and must call RequireUser before doing any work.
type Store interface {
	// ActiveConversation returns the user's open conversation, creating one if there is none.
	ActiveConversation(ctx context.Context, user identity.UserID) (Conversation, error)
	// CloseConversation ends the user's open conversation, if any.
	CloseConversation(ctx context.Context, user identity.UserID) error
	AppendMessage(ctx context.Context, user identity.UserID, conversationID int64, role model.Role, content string) (Message, error)
	// RecentMessages returns up to limit of the newest messages, oldest first.
	RecentMessages(ctx context.Context, user identity.UserID, conversationID int64, limit int) ([]Message, error)
	RecordModelCall(ctx context.Context, user identity.UserID, call ModelCall) error
	RecordToolCall(ctx context.Context, user identity.UserID, call ToolCall) error
	// ProposeAction stores a new pending action owned by user.
	ProposeAction(ctx context.Context, user identity.UserID, action PendingAction) error
	// GetPendingAction returns one of the user's pending actions, or ErrActionNotFound.
	GetPendingAction(ctx context.Context, user identity.UserID, id string) (PendingAction, error)
	// UpdatePendingAction applies update only while the user's action is in state from. It
	// reports false when it is in another state, which is how a repeated decision loses the race.
	UpdatePendingAction(ctx context.Context, user identity.UserID, id, from string, update PendingUpdate) (bool, error)
	// FailStaleApprovals marks every action approved before cutoff as failed as outcome unknown,
	// for all users, and returns those actions. It is the one method without a user ID: it reads
	// no user data beyond what the returned rows carry, and changes only approved rows past cutoff.
	FailStaleApprovals(ctx context.Context, cutoff time.Time) ([]FailedApproval, error)
	// UnnotifiedOutcomes returns the user's actions failed as outcome unknown whose notice was not
	// delivered yet.
	UnnotifiedOutcomes(ctx context.Context, user identity.UserID) ([]PendingAction, error)
	// MarkOutcomeNoticed records that the outcome-unknown notice for the action was delivered.
	MarkOutcomeNoticed(ctx context.Context, user identity.UserID, id string, at time.Time) error
	// SupersedePending marks the user's other proposed actions for tool as superseded by keepID,
	// and returns how many it changed.
	SupersedePending(ctx context.Context, user identity.UserID, tool, keepID string) (int64, error)
	// MarkCardSent records that the action's card was delivered, to chatID.
	MarkCardSent(ctx context.Context, user identity.UserID, id string, chatID int64, at time.Time) error
	// UnsentCards returns the user's proposed, unexpired actions whose cards were not delivered.
	UnsentCards(ctx context.Context, user identity.UserID, now time.Time) ([]PendingAction, error)

	// AddTask stores a task and returns it with its ID.
	AddTask(ctx context.Context, user identity.UserID, task Task) (Task, error)
	// ListTasks returns the user's tasks, open ones first in due-day order with undated last. Done
	// tasks are included only when includeDone is set.
	ListTasks(ctx context.Context, user identity.UserID, includeDone bool) ([]Task, error)
	// GetTask returns one of the user's tasks, or ErrTaskNotFound.
	GetTask(ctx context.Context, user identity.UserID, id int64) (Task, error)
	// CompleteTask marks one of the user's tasks done at the given time. Completing a done task
	// changes nothing.
	CompleteTask(ctx context.Context, user identity.UserID, id int64, at time.Time) error
	// DeleteTask removes one of the user's tasks.
	DeleteTask(ctx context.Context, user identity.UserID, id int64) error

	// AddReminder stores a reminder and returns it with its ID.
	AddReminder(ctx context.Context, user identity.UserID, reminder Reminder) (Reminder, error)
	// GetReminder returns one of the user's reminders, or ErrReminderNotFound.
	GetReminder(ctx context.Context, user identity.UserID, id int64) (Reminder, error)
	// ListReminders returns the user's pending reminders, earliest first.
	ListReminders(ctx context.Context, user identity.UserID) ([]Reminder, error)
	// DueReminders returns the user's pending reminders whose time has come by now.
	DueReminders(ctx context.Context, user identity.UserID, now time.Time) ([]Reminder, error)
	// CancelReminder cancels one of the user's pending reminders. Otherwise ErrReminderNotFound.
	CancelReminder(ctx context.Context, user identity.UserID, id int64, at time.Time) error
	// MarkReminderDelivered records that a pending reminder was sent. It reports false when the
	// reminder was no longer pending.
	MarkReminderDelivered(ctx context.Context, user identity.UserID, id int64, at time.Time) (bool, error)
	// UsersWithDueReminders returns the IDs of the users that have a pending reminder due by now,
	// for all users. It is the one reminder query without a user ID: it returns IDs only, and each
	// reminder is then read with its owner in context.
	UsersWithDueReminders(ctx context.Context, now time.Time) ([]identity.UserID, error)
}

// RequireUser returns an error unless ctx carries user, so that a call made outside a user's
// request performs no query.
func RequireUser(ctx context.Context, user identity.UserID) error {
	got, err := identity.UserFrom(ctx)
	if err != nil {
		return err
	}
	if got != user {
		return ErrUserMismatch
	}
	return nil
}
