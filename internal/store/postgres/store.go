package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the PostgreSQL store.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store using pool.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Open connects to the database at url and applies pending migrations. The caller closes the
// returned Store.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return New(pool), nil
}

// Close releases the database connections.
func (s *Store) Close() {
	s.pool.Close()
}

// AllowedIdentity is one allowlist entry of a platform.
type AllowedIdentity struct {
	ExternalID  string
	DisplayName string
}

// SyncAllowlist makes the platform's identities match allowed: new entries get a user, existing
// ones are enabled and renamed, and identities not listed are disabled with their data kept. It
// runs at startup on behalf of no user, so it is the one method that takes no user ID.
func (s *Store) SyncAllowlist(ctx context.Context, platform string, allowed []AllowedIdentity) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		externalIDs := make([]string, 0, len(allowed))
		for _, identity := range allowed {
			if err := upsertIdentity(ctx, tx, platform, identity); err != nil {
				return err
			}
			externalIDs = append(externalIDs, identity.ExternalID)
		}
		_, err := tx.Exec(ctx, `
			UPDATE user_identities SET disabled_at = now()
			WHERE platform = $1 AND disabled_at IS NULL AND NOT external_id = ANY($2)`,
			platform, externalIDs)
		if err != nil {
			return fmt.Errorf("disable removed identities: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("sync allowlist: %w", err)
	}
	return nil
}

func upsertIdentity(ctx context.Context, tx pgx.Tx, platform string, identity AllowedIdentity) error {
	var userID int64
	err := tx.QueryRow(ctx, `
		UPDATE user_identities SET disabled_at = NULL
		WHERE platform = $1 AND external_id = $2
		RETURNING user_id`,
		platform, identity.ExternalID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return insertIdentity(ctx, tx, platform, identity)
	}
	if err != nil {
		return fmt.Errorf("enable identity: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET display_name = $1 WHERE id = $2`, identity.DisplayName, userID); err != nil {
		return fmt.Errorf("rename user: %w", err)
	}
	return nil
}

func insertIdentity(ctx context.Context, tx pgx.Tx, platform string, identity AllowedIdentity) error {
	var userID int64
	err := tx.QueryRow(ctx, `INSERT INTO users (display_name) VALUES ($1) RETURNING id`,
		identity.DisplayName).Scan(&userID)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_identities (platform, external_id, user_id) VALUES ($1, $2, $3)`,
		platform, identity.ExternalID, userID)
	if err != nil {
		return fmt.Errorf("insert identity: %w", err)
	}
	return nil
}
