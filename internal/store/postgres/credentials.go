package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/store"
)

// SetCredential implements store.Store. It replaces any earlier value for the same key.
func (s *Store) SetCredential(ctx context.Context, user identity.UserID, key string, ciphertext, nonce []byte) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_credentials (user_id, key, ciphertext, nonce, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, key)
		DO UPDATE SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`,
		user, key, ciphertext, nonce)
	if err != nil {
		return fmt.Errorf("set credential: %w", err)
	}
	return nil
}

// GetCredential implements store.Store.
func (s *Store) GetCredential(ctx context.Context, user identity.UserID, key string) (store.Credential, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Credential{}, err
	}
	var c store.Credential
	err := s.pool.QueryRow(ctx, `
		SELECT ciphertext, nonce, updated_at FROM user_credentials
		WHERE user_id = $1 AND key = $2`, user, key).Scan(&c.Ciphertext, &c.Nonce, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Credential{}, store.ErrCredentialNotFound
	}
	if err != nil {
		return store.Credential{}, fmt.Errorf("get credential: %w", err)
	}
	return c, nil
}
