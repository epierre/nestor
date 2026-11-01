// Package testutil provides in-memory fakes of the package boundary interfaces.
package testutil

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/epierre/nestor/internal/channel"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

var (
	_ channel.Channel   = (*FakeChannel)(nil)
	_ model.ModelClient = (*FakeModel)(nil)
	_ store.Store       = (*FakeStore)(nil)
)

// FakeChannel delivers Inbound as messages from User, then returns from Run.
type FakeChannel struct {
	User    identity.UserID
	ChatID  int64
	Inbound []string

	mu   sync.Mutex
	sent []string
}

// Run hands each inbound message to h and sends the reply, stopping at the first error.
func (c *FakeChannel) Run(ctx context.Context, h channel.Handler) error {
	ctx = identity.WithUser(ctx, c.User)
	for _, text := range c.Inbound {
		reply, err := h.HandleMessage(ctx, text)
		if err != nil {
			return err
		}
		if err := c.Send(ctx, c.ChatID, reply); err != nil {
			return err
		}
	}
	return nil
}

// Send records text.
func (c *FakeChannel) Send(_ context.Context, _ int64, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, text)
	return nil
}

// Sent returns the messages sent so far.
func (c *FakeChannel) Sent() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.sent)
}

// FakeModel answers each request with Reply, or an empty response if Reply is nil.
type FakeModel struct {
	Reply func(model.CompletionRequest) (model.CompletionResponse, error)

	mu       sync.Mutex
	requests []model.CompletionRequest
}

// Complete records req and returns what Reply produces for it. Like a real client it reports the
// attempt it made, unless Reply scripts its own Attempts.
func (m *FakeModel) Complete(_ context.Context, req model.CompletionRequest) (model.CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	var resp model.CompletionResponse
	var err error
	if m.Reply != nil {
		resp, err = m.Reply(req)
	}
	if len(resp.Attempts) == 0 {
		attempt, encodeErr := fakeAttempt(req, resp, err)
		if encodeErr != nil {
			return model.CompletionResponse{}, encodeErr
		}
		resp.Attempts = []model.Attempt{attempt}
	}
	return resp, err
}

func fakeAttempt(req model.CompletionRequest, resp model.CompletionResponse, callErr error) (model.Attempt, error) {
	request, err := json.Marshal(req)
	if err != nil {
		return model.Attempt{}, err
	}
	attempt := model.Attempt{
		StartedAt: time.Now(),
		Latency:   time.Millisecond,
		Request:   request,
		Err:       callErr,
	}
	if callErr == nil {
		if attempt.Response, err = json.Marshal(resp); err != nil {
			return model.Attempt{}, err
		}
	}
	return attempt, nil
}

// Requests returns the requests received so far.
func (m *FakeModel) Requests() []model.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

// FakeStore keeps data in memory and enforces the same user scoping as a real store. Sharing one
// FakeStore between two agents stands in for a process restart.
type FakeStore struct {
	mu            sync.Mutex
	nextID        int64
	conversations []store.Conversation
	messages      []store.Message
	modelCalls    []store.ModelCall
}

// NewFakeStore returns an empty FakeStore.
func NewFakeStore() *FakeStore {
	return &FakeStore{}
}

func (s *FakeStore) id() int64 {
	s.nextID++
	return s.nextID
}

func (s *FakeStore) open(user identity.UserID) *store.Conversation {
	for i := range s.conversations {
		c := &s.conversations[i]
		if c.UserID == user && c.EndedAt == nil {
			return c
		}
	}
	return nil
}

func (s *FakeStore) owns(user identity.UserID, conversationID int64) bool {
	return slices.ContainsFunc(s.conversations, func(c store.Conversation) bool {
		return c.ID == conversationID && c.UserID == user
	})
}

// ActiveConversation implements store.Store.
func (s *FakeStore) ActiveConversation(ctx context.Context, user identity.UserID) (store.Conversation, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Conversation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.open(user); c != nil {
		return *c, nil
	}
	c := store.Conversation{ID: s.id(), UserID: user, StartedAt: time.Now()}
	s.conversations = append(s.conversations, c)
	return c, nil
}

// CloseConversation implements store.Store.
func (s *FakeStore) CloseConversation(ctx context.Context, user identity.UserID) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.open(user); c != nil {
		now := time.Now()
		c.EndedAt = &now
	}
	return nil
}

// AppendMessage implements store.Store.
func (s *FakeStore) AppendMessage(ctx context.Context, user identity.UserID, conversationID int64, role model.Role, content string) (store.Message, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Message{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.owns(user, conversationID) {
		return store.Message{}, store.ErrConversationNotFound
	}
	m := store.Message{
		ID:             s.id(),
		UserID:         user,
		ConversationID: conversationID,
		Role:           role,
		Content:        content,
		CreatedAt:      time.Now(),
	}
	s.messages = append(s.messages, m)
	return m, nil
}

// RecentMessages implements store.Store.
func (s *FakeStore) RecentMessages(ctx context.Context, user identity.UserID, conversationID int64, limit int) ([]store.Message, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []store.Message
	for _, m := range s.messages {
		if m.UserID == user && m.ConversationID == conversationID {
			found = append(found, m)
		}
	}
	return found[max(0, len(found)-limit):], nil
}

// RecordModelCall implements store.Store.
func (s *FakeStore) RecordModelCall(ctx context.Context, user identity.UserID, call store.ModelCall) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.owns(user, call.ConversationID) {
		return store.ErrConversationNotFound
	}
	call.ID = s.id()
	call.UserID = user
	s.modelCalls = append(s.modelCalls, call)
	return nil
}

// ModelCalls returns the calls recorded for user.
func (s *FakeStore) ModelCalls(user identity.UserID) []store.ModelCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []store.ModelCall
	for _, c := range s.modelCalls {
		if c.UserID == user {
			found = append(found, c)
		}
	}
	return found
}
