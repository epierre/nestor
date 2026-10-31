package testutil

import (
	"context"
	"errors"
	"testing"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

func TestFakeStoreUnscopedCallReturnsError(t *testing.T) {
	s := NewFakeStore()
	ctx := context.Background()

	if _, err := s.ActiveConversation(ctx, 1); !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("ActiveConversation: got error %v, want ErrNoUser", err)
	}
	if err := s.CloseConversation(ctx, 1); !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("CloseConversation: got error %v, want ErrNoUser", err)
	}
	if _, err := s.AppendMessage(ctx, 1, 1, model.RoleUser, "hi"); !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("AppendMessage: got error %v, want ErrNoUser", err)
	}
	if _, err := s.RecentMessages(ctx, 1, 1, 10); !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("RecentMessages: got error %v, want ErrNoUser", err)
	}
	if err := s.RecordModelCall(ctx, 1, store.ModelCall{}); !errors.Is(err, identity.ErrNoUser) {
		t.Errorf("RecordModelCall: got error %v, want ErrNoUser", err)
	}
}

func TestFakeStoreKeepsUsersApart(t *testing.T) {
	s := NewFakeStore()
	alice := identity.WithUser(context.Background(), 1)
	bob := identity.WithUser(context.Background(), 2)

	conv, err := s.ActiveConversation(alice, 1)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	if _, err := s.AppendMessage(alice, 1, conv.ID, model.RoleUser, "falcon"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	if _, err := s.AppendMessage(bob, 2, conv.ID, model.RoleUser, "intruder"); err == nil {
		t.Error("user 2 appended to user 1's conversation")
	}
	got, err := s.RecentMessages(bob, 2, conv.ID, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("user 2 read %d of user 1's messages", len(got))
	}
}

func TestFakeStoreRecentMessagesKeepsNewestInOrder(t *testing.T) {
	s := NewFakeStore()
	ctx := identity.WithUser(context.Background(), 1)
	conv, err := s.ActiveConversation(ctx, 1)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	for _, text := range []string{"one", "two", "three"} {
		if _, err := s.AppendMessage(ctx, 1, conv.ID, model.RoleUser, text); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}

	got, err := s.RecentMessages(ctx, 1, conv.ID, 2)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(got) != 2 || got[0].Content != "two" || got[1].Content != "three" {
		t.Errorf("got %+v, want two then three", got)
	}
}

func TestFakeStoreCloseConversationStartsFresh(t *testing.T) {
	s := NewFakeStore()
	ctx := identity.WithUser(context.Background(), 1)
	first, err := s.ActiveConversation(ctx, 1)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	if err := s.CloseConversation(ctx, 1); err != nil {
		t.Fatalf("CloseConversation: %v", err)
	}

	second, err := s.ActiveConversation(ctx, 1)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	if second.ID == first.ID {
		t.Errorf("conversation %d was reused after close", first.ID)
	}
}
