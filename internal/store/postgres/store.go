package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

const foreignKeyViolation = "23503"

var _ store.Store = (*Store)(nil)

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
// runs at startup on behalf of no user, so it takes no user ID.
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

// ResolveIdentity returns the user behind an enabled platform identity, or
// store.ErrIdentityNotFound. Like SyncAllowlist it runs before any user is known: this lookup is
// what establishes the user for a request.
func (s *Store) ResolveIdentity(ctx context.Context, platform, externalID string) (identity.UserID, error) {
	var user identity.UserID
	err := s.pool.QueryRow(ctx, `
		SELECT i.user_id FROM user_identities i JOIN users u ON u.id = i.user_id
		WHERE i.platform = $1 AND i.external_id = $2
			AND i.disabled_at IS NULL AND u.disabled_at IS NULL`,
		platform, externalID).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, store.ErrIdentityNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve identity: %w", err)
	}
	return user, nil
}

// ActiveConversation implements store.Store. Concurrent calls for one user get the same
// conversation: the insert yields to the unique index on open conversations.
func (s *Store) ActiveConversation(ctx context.Context, user identity.UserID) (store.Conversation, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Conversation{}, err
	}
	c, err := s.openConversation(ctx, user)
	if !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO conversations (user_id) VALUES ($1)
		ON CONFLICT (user_id) WHERE ended_at IS NULL DO NOTHING`, user)
	if err != nil {
		return store.Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return s.openConversation(ctx, user)
}

func (s *Store) openConversation(ctx context.Context, user identity.UserID) (store.Conversation, error) {
	c := store.Conversation{UserID: user}
	err := s.pool.QueryRow(ctx, `
		SELECT id, started_at, ended_at FROM conversations
		WHERE user_id = $1 AND ended_at IS NULL`, user).Scan(&c.ID, &c.StartedAt, &c.EndedAt)
	if err != nil {
		return store.Conversation{}, fmt.Errorf("find open conversation: %w", err)
	}
	return c, nil
}

// CloseConversation implements store.Store.
func (s *Store) CloseConversation(ctx context.Context, user identity.UserID) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE conversations SET ended_at = now()
		WHERE user_id = $1 AND ended_at IS NULL`, user)
	if err != nil {
		return fmt.Errorf("close conversation: %w", err)
	}
	return nil
}

// AppendMessage implements store.Store.
func (s *Store) AppendMessage(ctx context.Context, user identity.UserID, conversationID int64, role model.Role, content string) (store.Message, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return store.Message{}, err
	}
	m := store.Message{UserID: user, ConversationID: conversationID, Role: role, Content: content}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO messages (user_id, conversation_id, role, content)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		user, conversationID, role, content).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return store.Message{}, fmt.Errorf("append message: %w", conversationError(err))
	}
	return m, nil
}

// RecentMessages implements store.Store.
func (s *Store) RecentMessages(ctx context.Context, user identity.UserID, conversationID int64, limit int) ([]store.Message, error) {
	if err := store.RequireUser(ctx, user); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, conversation_id, role, content, created_at FROM (
			SELECT * FROM messages
			WHERE user_id = $1 AND conversation_id = $2
			ORDER BY id DESC LIMIT $3
		) AS recent ORDER BY id`,
		user, conversationID, limit)
	if err != nil {
		return nil, fmt.Errorf("query messages: %w", err)
	}
	messages, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (store.Message, error) {
		var m store.Message
		err := row.Scan(&m.ID, &m.UserID, &m.ConversationID, &m.Role, &m.Content, &m.CreatedAt)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("read messages: %w", err)
	}
	return messages, nil
}

// RecordModelCall implements store.Store. Latency is stored in milliseconds, rounded up from
// below one so that a call that took time never reads as zero.
func (s *Store) RecordModelCall(ctx context.Context, user identity.UserID, call store.ModelCall) error {
	if err := store.RequireUser(ctx, user); err != nil {
		return err
	}
	latency := call.Latency.Milliseconds()
	if latency == 0 && call.Latency > 0 {
		latency = 1
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO model_calls (user_id, conversation_id, model, started_at, latency_ms,
			prompt_tokens, completion_tokens, cost_usd, status, error, request_json, response_json)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		user, call.ConversationID, call.Model, call.StartedAt, latency,
		call.PromptTokens, call.CompletionTokens, call.CostUSD, call.Status, call.Error,
		call.RequestJSON, call.ResponseJSON)
	if err != nil {
		return fmt.Errorf("record model call: %w", conversationError(err))
	}
	return nil
}

// conversationError turns the composite foreign key's refusal into ErrConversationNotFound.
func conversationError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation {
		return store.ErrConversationNotFound
	}
	return err
}
