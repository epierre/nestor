// Package store defines persistence for user data. Every method is scoped to one user.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
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
