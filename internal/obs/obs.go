// Package obs ties log lines to the message that caused them.
package obs

import (
	"context"
	"crypto/rand"
	"log/slog"

	"github.com/epierre/nestor/internal/identity"
)

type contextKey struct{}

// NewRequestID returns a random ID for one incoming message.
func NewRequestID() string {
	return rand.Text()
}

// WithRequestID returns a copy of ctx carrying id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// Handler adds the request ID and user ID carried by a record's context to the line, as top-level
// fields. Log with the *Context functions of slog for them to appear.
type Handler struct {
	inner   slog.Handler
	derived []func(slog.Handler) slog.Handler // WithAttrs and WithGroup calls, replayed in order
}

// NewHandler returns a Handler writing to inner.
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, record slog.Record) error {
	inner := h.inner
	if id, ok := ctx.Value(contextKey{}).(string); ok {
		inner = inner.WithAttrs([]slog.Attr{slog.String("request_id", id)})
	}
	if user, err := identity.UserFrom(ctx); err == nil {
		inner = inner.WithAttrs([]slog.Attr{slog.Int64("user_id", int64(user))})
	}
	for _, apply := range h.derived {
		inner = apply(inner)
	}
	return inner.Handle(ctx, record)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h.with(func(inner slog.Handler) slog.Handler { return inner.WithAttrs(attrs) })
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	return h.with(func(inner slog.Handler) slog.Handler { return inner.WithGroup(name) })
}

func (h *Handler) with(apply func(slog.Handler) slog.Handler) *Handler {
	derived := make([]func(slog.Handler) slog.Handler, 0, len(h.derived)+1)
	return &Handler{inner: h.inner, derived: append(append(derived, h.derived...), apply)}
}
