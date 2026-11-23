package testutil

import (
	"context"
	"slices"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

type credentialKey struct {
	user identity.UserID
	key  string
}

// SetCredential implements store.Store.
func (s *FakeStore) SetCredential(ctx context.Context, user identity.UserID, key string, ciphertext, nonce []byte) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credentials == nil {
		s.credentials = make(map[credentialKey]store.Credential)
	}
	s.credentials[credentialKey{user, key}] = store.Credential{
		Ciphertext: slices.Clone(ciphertext),
		Nonce:      slices.Clone(nonce),
	}
	return nil
}

// GetCredential implements store.Store.
func (s *FakeStore) GetCredential(ctx context.Context, user identity.UserID, key string) (store.Credential, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Credential{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.credentials[credentialKey{user, key}]
	if !ok {
		return store.Credential{}, store.ErrCredentialNotFound
	}
	return c, nil
}
