package identity

import (
	"context"
	"errors"
	"testing"
)

func TestUserFromCarriedUser(t *testing.T) {
	got, err := UserFrom(WithUser(context.Background(), 42))
	if err != nil {
		t.Fatalf("UserFrom: %v", err)
	}
	if got != 42 {
		t.Errorf("got user %d, want 42", got)
	}
}

func TestUserFromMissingUserReturnsError(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
	}{
		{"no user in context", context.Background()},
		{"zero user ID", WithUser(context.Background(), 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := UserFrom(tt.ctx); !errors.Is(err, ErrNoUser) {
				t.Errorf("got error %v, want ErrNoUser", err)
			}
		})
	}
}
