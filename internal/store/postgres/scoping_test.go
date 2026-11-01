package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

// A Store without a pool panics on any query, so an error here proves none was attempted.
func TestUnscopedCallReturnsError(t *testing.T) {
	const user identity.UserID = 1
	st := New(nil)
	methods := []struct {
		name string
		call func(ctx context.Context) error
	}{
		{"ActiveConversation", func(ctx context.Context) error {
			_, err := st.ActiveConversation(ctx, user)
			return err
		}},
		{"CloseConversation", func(ctx context.Context) error {
			return st.CloseConversation(ctx, user)
		}},
		{"AppendMessage", func(ctx context.Context) error {
			_, err := st.AppendMessage(ctx, user, 1, model.RoleUser, "hello")
			return err
		}},
		{"RecentMessages", func(ctx context.Context) error {
			_, err := st.RecentMessages(ctx, user, 1, 10)
			return err
		}},
		{"RecordModelCall", func(ctx context.Context) error {
			return st.RecordModelCall(ctx, user, store.ModelCall{})
		}},
	}
	contexts := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"no user", context.Background(), identity.ErrNoUser},
		{"another user", identity.WithUser(context.Background(), 2), store.ErrUserMismatch},
	}
	for _, m := range methods {
		for _, c := range contexts {
			t.Run(m.name+"/"+c.name, func(t *testing.T) {
				if err := m.call(c.ctx); !errors.Is(err, c.want) {
					t.Errorf("got error %v, want %v", err, c.want)
				}
			})
		}
	}
}
