package channel

import "context"

type chatKey struct{}

// WithChat returns a copy of ctx carrying the chat that the current update came from. The chat is
// an address, not an identity: it is recorded so that notices for an action reach the same chat.
func WithChat(ctx context.Context, chatID int64) context.Context {
	return context.WithValue(ctx, chatKey{}, chatID)
}

// ChatFrom returns the chat carried by ctx, if any.
func ChatFrom(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(chatKey{}).(int64)
	return id, ok
}
