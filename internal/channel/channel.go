// Package channel defines how users reach the assistant. A channel is the only code that knows
// platform identities.
package channel

import "context"

// Handler processes one incoming message and returns the reply. The context carries the sender's
// internal user ID; the channel resolves it before calling HandleMessage.
type Handler interface {
	HandleMessage(ctx context.Context, text string) (string, error)
}

// Channel delivers messages from allowed users to a Handler and sends replies back.
type Channel interface {
	// Run blocks, delivering messages to h, until ctx is cancelled.
	Run(ctx context.Context, h Handler) error
	Send(ctx context.Context, chatID int64, text string) error
}
