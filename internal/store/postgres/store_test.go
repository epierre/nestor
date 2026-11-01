//go:build integration

package postgres

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
	checkViolation      = "23514"
)

var appTables = []string{
	"conversations", "messages", "model_calls", "schema_migrations", "user_identities", "users",
}

func tables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY 1`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return names
}

func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := newDatabase(t)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return pool
}

func insertID(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return id
}

func twoUsers(t *testing.T, pool *pgxpool.Pool) (a, b int64) {
	t.Helper()
	const insert = `INSERT INTO users (display_name) VALUES ($1) RETURNING id`
	return insertID(t, pool, insert, "Ana"), insertID(t, pool, insert, "Ben")
}

func openConversation(t *testing.T, pool *pgxpool.Pool, user int64) int64 {
	t.Helper()
	return insertID(t, pool, `INSERT INTO conversations (user_id) VALUES ($1) RETURNING id`, user)
}

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("got error %v, want Postgres error %s", err, want)
	}
	if pgErr.Code != want {
		t.Errorf("got Postgres error %s (%s), want %s", pgErr.Code, pgErr.Message, want)
	}
}

func TestMigrateUpAndDown(t *testing.T) {
	ctx := context.Background()
	pool := newDatabase(t)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := tables(t, pool); !reflect.DeepEqual(got, appTables) {
		t.Errorf("after up: tables %v, want %v", got, appTables)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	if err := MigrateDown(ctx, pool); err != nil {
		t.Fatalf("MigrateDown: %v", err)
	}
	if got, want := tables(t, pool), []string{"schema_migrations"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after down: tables %v, want %v", got, want)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate after down: %v", err)
	}
	if got := tables(t, pool); !reflect.DeepEqual(got, appTables) {
		t.Errorf("after second up: tables %v, want %v", got, appTables)
	}
}

func TestMigrateConcurrently(t *testing.T) {
	pool := newDatabase(t)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() { errs[i] = Migrate(context.Background(), pool) })
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := tables(t, pool); !reflect.DeepEqual(got, appTables) {
		t.Errorf("tables %v, want %v", got, appTables)
	}
}

func TestFailedMigrationLeavesNothingApplied(t *testing.T) {
	ctx := context.Background()
	pool := newDatabase(t)

	err := migrateUp(ctx, pool, []migration{
		{version: 1, up: `CREATE TABLE first (id int)`},
		{version: 2, up: `NOT VALID SQL`},
	})
	if err == nil {
		t.Fatal("migrateUp succeeded, want error")
	}

	if got := tables(t, pool); len(got) != 0 {
		t.Errorf("got tables %v, want none", got)
	}
}

func TestUserDataTablesRequireUserID(t *testing.T) {
	pool := migrated(t)

	for _, table := range []string{"user_identities", "conversations", "messages", "model_calls"} {
		t.Run(table, func(t *testing.T) {
			var nullable string
			err := pool.QueryRow(context.Background(), `
				SELECT is_nullable FROM information_schema.columns
				WHERE table_schema = 'public' AND table_name = $1 AND column_name = 'user_id'`,
				table).Scan(&nullable)
			if err != nil {
				t.Fatalf("find user_id: %v", err)
			}
			if nullable != "NO" {
				t.Errorf("user_id is_nullable = %s, want NO", nullable)
			}
		})
	}
}

func TestChildRowsStayInTheirParentsUser(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"message", `INSERT INTO messages (user_id, conversation_id, role, content)
			VALUES ($1, $2, 'user', 'hello')`},
		{"model call", `INSERT INTO model_calls (user_id, conversation_id, model, started_at,
			latency_ms, status, request_json) VALUES ($1, $2, 'cheap', now(), 1, 'ok', '{}')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := migrated(t)
			ana, ben := twoUsers(t, pool)
			conversation := openConversation(t, pool, ana)

			if _, err := pool.Exec(ctx, tt.sql, ana, conversation); err != nil {
				t.Fatalf("insert for the conversation's user: %v", err)
			}

			_, err := pool.Exec(ctx, tt.sql, ben, conversation)
			assertCode(t, err, foreignKeyViolation)
		})
	}
}

func TestOneOpenConversationPerUser(t *testing.T) {
	ctx := context.Background()
	pool := migrated(t)
	ana, ben := twoUsers(t, pool)
	first := openConversation(t, pool, ana)

	_, err := pool.Exec(ctx, `INSERT INTO conversations (user_id) VALUES ($1)`, ana)
	assertCode(t, err, uniqueViolation)

	openConversation(t, pool, ben)

	if _, err := pool.Exec(ctx, `UPDATE conversations SET ended_at = now() WHERE id = $1`, first); err != nil {
		t.Fatalf("close conversation: %v", err)
	}
	openConversation(t, pool, ana)
}

func TestIdentityBelongsToOneUser(t *testing.T) {
	ctx := context.Background()
	pool := migrated(t)
	ana, ben := twoUsers(t, pool)
	const insert = `INSERT INTO user_identities (platform, external_id, user_id) VALUES ('telegram', '100', $1)`

	if _, err := pool.Exec(ctx, insert, ana); err != nil {
		t.Fatalf("insert identity: %v", err)
	}

	_, err := pool.Exec(ctx, insert, ben)
	assertCode(t, err, uniqueViolation)
}

func TestInvalidValuesRejected(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{"message role", `INSERT INTO messages (user_id, conversation_id, role, content)
			VALUES ($1, $2, 'owner', 'hello')`},
		{"model call status", `INSERT INTO model_calls (user_id, conversation_id, model, started_at,
			latency_ms, status, request_json) VALUES ($1, $2, 'cheap', now(), 1, 'maybe', '{}')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := migrated(t)
			ana, _ := twoUsers(t, pool)

			_, err := pool.Exec(context.Background(), tt.sql, ana, openConversation(t, pool, ana))
			assertCode(t, err, checkViolation)
		})
	}
}

func TestModelCallCostStaysNull(t *testing.T) {
	ctx := context.Background()
	pool := migrated(t)
	ana, _ := twoUsers(t, pool)

	id := insertID(t, pool, `INSERT INTO model_calls (user_id, conversation_id, model, started_at,
		latency_ms, status, request_json) VALUES ($1, $2, 'cheap', now(), 1, 'ok', '{}') RETURNING id`,
		ana, openConversation(t, pool, ana))

	var cost *float64
	if err := pool.QueryRow(ctx, `SELECT cost_usd FROM model_calls WHERE id = $1`, id).Scan(&cost); err != nil {
		t.Fatalf("read cost: %v", err)
	}
	if cost != nil {
		t.Errorf("got cost %v, want null", *cost)
	}
}
