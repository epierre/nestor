// Package identity carries the internal user ID through a request context. The channel resolves
// platform identities once, at the boundary; everything below it only sees a UserID.
package identity

import (
	"context"
	"errors"
)

// UserID is the internal ID of a user. It is never a platform identity such as a Telegram ID.
// The zero value means no user.
type UserID int64

// ErrNoUser is returned when a context carries no user ID.
var ErrNoUser = errors.New("no user ID in context")

type contextKey struct{}

// WithUser returns a copy of ctx carrying id.
func WithUser(ctx context.Context, id UserID) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// UserFrom returns the user ID carried by ctx, or ErrNoUser.
func UserFrom(ctx context.Context) (UserID, error) {
	id, ok := ctx.Value(contextKey{}).(UserID)
	if !ok || id == 0 {
		return 0, ErrNoUser
	}
	return id, nil
}
