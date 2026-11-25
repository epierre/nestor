package store

import (
	"context"
	"errors"
)

// ErrNoConversation is returned when a context carries no active conversation ID.
var ErrNoConversation = errors.New("no conversation in context")

type conversationKey struct{}

// WithConversation returns a copy of ctx carrying conversationID.
func WithConversation(ctx context.Context, conversationID int64) context.Context {
	return context.WithValue(ctx, conversationKey{}, conversationID)
}

// ConversationFrom returns the active conversation ID carried by ctx, or ErrNoConversation.
func ConversationFrom(ctx context.Context) (int64, error) {
	id, ok := ctx.Value(conversationKey{}).(int64)
	if !ok || id <= 0 {
		return 0, ErrNoConversation
	}
	return id, nil
}
