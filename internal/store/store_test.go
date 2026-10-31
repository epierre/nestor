package store

import (
	"context"
	"errors"
	"testing"

	"github.com/epierre/nestor/internal/identity"
)

func TestRequireUser(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		user identity.UserID
		want error
	}{
		{"matching user", identity.WithUser(context.Background(), 1), 1, nil},
		{"no user in context", context.Background(), 1, identity.ErrNoUser},
		{"different user", identity.WithUser(context.Background(), 2), 1, ErrUserMismatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := RequireUser(tt.ctx, tt.user); !errors.Is(err, tt.want) {
				t.Errorf("got error %v, want %v", err, tt.want)
			}
		})
	}
}
