//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	neturl "net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/epierre/nestor/internal/store"
)

const (
	uniqueViolation = "23505"
	checkViolation  = "23514"
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

type identityRow struct {
	UserID   int64
	Name     string
	Disabled bool
}

// identities returns the rows of platform keyed by external ID.
func identities(t *testing.T, pool *pgxpool.Pool, platform string) map[string]identityRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT i.external_id, i.user_id, u.display_name, i.disabled_at IS NOT NULL
		FROM user_identities i JOIN users u ON u.id = i.user_id
		WHERE i.platform = $1`, platform)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	defer rows.Close()
	found := make(map[string]identityRow)
	for rows.Next() {
		var id string
		var row identityRow
		if err := rows.Scan(&id, &row.UserID, &row.Name, &row.Disabled); err != nil {
			t.Fatalf("scan identity: %v", err)
		}
		found[id] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list identities: %v", err)
	}
	return found
}

func syncAllowlist(t *testing.T, st *Store, platform string, allowed ...AllowedIdentity) {
	t.Helper()
	if err := st.SyncAllowlist(context.Background(), platform, allowed); err != nil {
		t.Fatalf("SyncAllowlist: %v", err)
	}
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

var (
	ana = AllowedIdentity{ExternalID: "100", DisplayName: "Ana"}
	ben = AllowedIdentity{ExternalID: "200", DisplayName: "Ben"}
)

func TestAllowlistUpsertIdempotent(t *testing.T) {
	pool := migrated(t)
	st := New(pool)

	syncAllowlist(t, st, "telegram", ana, ben)
	first := identities(t, pool, "telegram")
	syncAllowlist(t, st, "telegram", ana, ben)

	if got := identities(t, pool, "telegram"); !reflect.DeepEqual(got, first) {
		t.Errorf("second sync changed identities: got %+v, want %+v", got, first)
	}
	if len(first) != 2 || first["100"].Name != "Ana" || first["200"].Name != "Ben" {
		t.Errorf("got identities %+v", first)
	}
	if first["100"].UserID == first["200"].UserID {
		t.Errorf("both identities share user %d", first["100"].UserID)
	}
	if got := count(t, pool, `SELECT count(*) FROM users`); got != 2 {
		t.Errorf("got %d users, want 2", got)
	}
}

func TestAllowlistRenamesUser(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	syncAllowlist(t, st, "telegram", ana)
	before := identities(t, pool, "telegram")["100"]

	syncAllowlist(t, st, "telegram", AllowedIdentity{ExternalID: "100", DisplayName: "Anna"})

	want := identityRow{UserID: before.UserID, Name: "Anna"}
	if got := identities(t, pool, "telegram")["100"]; got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAllowlistRemovedIdentityDisabledDataKept(t *testing.T) {
	ctx := context.Background()
	pool := migrated(t)
	st := New(pool)
	syncAllowlist(t, st, "telegram", ana, ben)
	benID := identities(t, pool, "telegram")["200"].UserID
	conversation := openConversation(t, pool, benID)
	_, err := pool.Exec(ctx, `INSERT INTO messages (user_id, conversation_id, role, content)
		VALUES ($1, $2, 'user', 'hello')`, benID, conversation)
	if err != nil {
		t.Fatalf("insert message: %v", err)
	}

	syncAllowlist(t, st, "telegram", ana)

	got := identities(t, pool, "telegram")
	if !got["200"].Disabled {
		t.Errorf("removed identity %+v is not disabled", got["200"])
	}
	if got["100"].Disabled {
		t.Errorf("kept identity %+v is disabled", got["100"])
	}
	if n := count(t, pool, `SELECT count(*) FROM messages WHERE user_id = $1`, benID); n != 1 {
		t.Errorf("removed user has %d messages, want 1", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM conversations WHERE user_id = $1`, benID); n != 1 {
		t.Errorf("removed user has %d conversations, want 1", n)
	}
}

func TestAllowlistKeepsFirstDisabledTime(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	syncAllowlist(t, st, "telegram", ana, ben)
	syncAllowlist(t, st, "telegram", ana)
	const query = `SELECT disabled_at FROM user_identities WHERE external_id = '200'`
	var first, second time.Time
	if err := pool.QueryRow(context.Background(), query).Scan(&first); err != nil {
		t.Fatalf("read disabled_at: %v", err)
	}

	syncAllowlist(t, st, "telegram", ana)

	if err := pool.QueryRow(context.Background(), query).Scan(&second); err != nil {
		t.Fatalf("read disabled_at: %v", err)
	}
	if !second.Equal(first) {
		t.Errorf("disabled_at moved from %v to %v", first, second)
	}
}

func TestAllowlistReAddedIdentityKeepsUser(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	syncAllowlist(t, st, "telegram", ana, ben)
	before := identities(t, pool, "telegram")["200"]
	syncAllowlist(t, st, "telegram", ana)

	syncAllowlist(t, st, "telegram", ana, ben)

	if got := identities(t, pool, "telegram")["200"]; got != before {
		t.Errorf("got %+v, want %+v", got, before)
	}
}

func TestAllowlistEmptyDisablesEveryone(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	syncAllowlist(t, st, "telegram", ana, ben)

	syncAllowlist(t, st, "telegram")

	for id, row := range identities(t, pool, "telegram") {
		if !row.Disabled {
			t.Errorf("identity %s is not disabled", id)
		}
	}
}

func TestAllowlistLeavesOtherPlatformsAlone(t *testing.T) {
	pool := migrated(t)
	st := New(pool)
	syncAllowlist(t, st, "signal", ana)

	syncAllowlist(t, st, "telegram", ben)

	if got := identities(t, pool, "signal")["100"]; got.Disabled {
		t.Errorf("other platform's identity %+v was disabled", got)
	}
}

func TestOpenMigrates(t *testing.T) {
	url, err := neturl.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	pool := newDatabase(t)
	url.Path = "/" + pool.Config().ConnConfig.Database

	st, err := Open(context.Background(), url.String())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()

	if got := tables(t, pool); !reflect.DeepEqual(got, appTables) {
		t.Errorf("tables %v, want %v", got, appTables)
	}
}

// twoScoped returns a store, two users and a context for each.
func twoScoped(t *testing.T) (st *Store, pool *pgxpool.Pool, a, b identity.UserID, ctxA, ctxB context.Context) {
	t.Helper()
	pool = migrated(t)
	first, second := twoUsers(t, pool)
	a, b = identity.UserID(first), identity.UserID(second)
	return New(pool), pool, a, b,
		identity.WithUser(context.Background(), a), identity.WithUser(context.Background(), b)
}

func active(t *testing.T, st *Store, ctx context.Context, user identity.UserID) store.Conversation {
	t.Helper()
	c, err := st.ActiveConversation(ctx, user)
	if err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	return c
}

func appendText(t *testing.T, st *Store, ctx context.Context, user identity.UserID, conversation int64, texts ...string) {
	t.Helper()
	for _, text := range texts {
		if _, err := st.AppendMessage(ctx, user, conversation, model.RoleUser, text); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
}

func contents(messages []store.Message) []string {
	texts := make([]string, 0, len(messages))
	for _, m := range messages {
		texts = append(texts, m.Content)
	}
	return texts
}

func TestActiveConversationPerUser(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)

	first := active(t, st, ctxA, a)
	if first.UserID != a || first.ID == 0 || first.StartedAt.IsZero() || first.EndedAt != nil {
		t.Errorf("got conversation %+v", first)
	}
	if again := active(t, st, ctxA, a); again.ID != first.ID {
		t.Errorf("second call returned conversation %d, want %d", again.ID, first.ID)
	}
	if other := active(t, st, ctxB, b); other.ID == first.ID || other.UserID != b {
		t.Errorf("user B got conversation %+v, user A has %+v", other, first)
	}
}

func TestActiveConversationConcurrently(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)

	var wg sync.WaitGroup
	ids := make([]int64, 8)
	errs := make([]error, len(ids))
	for i := range ids {
		wg.Go(func() {
			c, err := st.ActiveConversation(ctxA, a)
			ids[i], errs[i] = c.ID, err
		})
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		t.Fatalf("ActiveConversation: %v", err)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("got conversations %v, want one", ids)
		}
	}
}

func TestCloseConversationOnlyClosesOwn(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	first := active(t, st, ctxA, a)
	other := active(t, st, ctxB, b)

	if err := st.CloseConversation(ctxA, a); err != nil {
		t.Fatalf("CloseConversation: %v", err)
	}

	if got := active(t, st, ctxA, a); got.ID == first.ID {
		t.Errorf("user A still has conversation %d open", first.ID)
	}
	if got := active(t, st, ctxB, b); got.ID != other.ID {
		t.Errorf("user B moved from conversation %d to %d", other.ID, got.ID)
	}
}

func TestCloseConversationWithNoneOpen(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)

	if err := st.CloseConversation(ctxA, a); err != nil {
		t.Errorf("CloseConversation: %v", err)
	}
}

func TestAppendMessage(t *testing.T) {
	st, _, a, _, ctxA, _ := twoScoped(t)
	conversation := active(t, st, ctxA, a)

	got, err := st.AppendMessage(ctxA, a, conversation.ID, model.RoleAssistant, "hello")
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	if got.ID == 0 || got.UserID != a || got.ConversationID != conversation.ID ||
		got.Role != model.RoleAssistant || got.Content != "hello" || got.CreatedAt.IsZero() {
		t.Errorf("got message %+v", got)
	}
}

func TestAppendMessageToOtherUsersConversationRejected(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	conversation := active(t, st, ctxA, a)

	_, err := st.AppendMessage(ctxB, b, conversation.ID, model.RoleUser, "hello")

	if !errors.Is(err, store.ErrConversationNotFound) {
		t.Errorf("got error %v, want ErrConversationNotFound", err)
	}
}

func TestRecentMessages(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  []string
	}{
		{"within limit, oldest first", 10, []string{"one", "two", "three"}},
		{"at limit", 3, []string{"one", "two", "three"}},
		{"beyond limit keeps the newest", 2, []string{"two", "three"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, _, a, _, ctxA, _ := twoScoped(t)
			conversation := active(t, st, ctxA, a)
			appendText(t, st, ctxA, a, conversation.ID, "one", "two", "three")

			got, err := st.RecentMessages(ctxA, a, conversation.ID, tt.limit)
			if err != nil {
				t.Fatalf("RecentMessages: %v", err)
			}
			if !reflect.DeepEqual(contents(got), tt.want) {
				t.Errorf("got %q, want %q", contents(got), tt.want)
			}
		})
	}
}

func TestRecentMessagesNeverShowOtherUsersData(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	conversationA := active(t, st, ctxA, a)
	conversationB := active(t, st, ctxB, b)
	appendText(t, st, ctxA, a, conversationA.ID, "my code word is falcon")
	appendText(t, st, ctxB, b, conversationB.ID, "what is my code word?")

	own, err := st.RecentMessages(ctxB, b, conversationB.ID, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if want := []string{"what is my code word?"}; !reflect.DeepEqual(contents(own), want) {
		t.Errorf("got %q, want %q", contents(own), want)
	}

	other, err := st.RecentMessages(ctxB, b, conversationA.ID, 10)
	if err != nil {
		t.Fatalf("RecentMessages: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("user B read %q from user A's conversation", contents(other))
	}
}

func TestRecordModelCall(t *testing.T) {
	cost := 0.00042
	st, pool, a, _, ctxA, _ := twoScoped(t)
	conversation := active(t, st, ctxA, a)
	started := time.Now().Truncate(time.Microsecond)

	err := st.RecordModelCall(ctxA, a, store.ModelCall{
		ConversationID:   conversation.ID,
		Model:            "cheap",
		StartedAt:        started,
		Latency:          1500 * time.Millisecond,
		PromptTokens:     12,
		CompletionTokens: 3,
		CostUSD:          &cost,
		Status:           "ok",
		RequestJSON:      json.RawMessage(`{"model": "cheap"}`),
		ResponseJSON:     json.RawMessage(`{"choices": []}`),
	})
	if err != nil {
		t.Fatalf("RecordModelCall: %v", err)
	}

	var got struct {
		user               int64
		model              string
		started            time.Time
		latency            int64
		prompt, completion int
		cost               float64
		status, errText    string
		request, response  string
	}
	err = pool.QueryRow(context.Background(), `
		SELECT user_id, model, started_at, latency_ms, prompt_tokens, completion_tokens, cost_usd,
			status, error, request_json::text, response_json::text
		FROM model_calls WHERE conversation_id = $1`, conversation.ID).Scan(
		&got.user, &got.model, &got.started, &got.latency, &got.prompt, &got.completion, &got.cost,
		&got.status, &got.errText, &got.request, &got.response)
	if err != nil {
		t.Fatalf("read call: %v", err)
	}
	if got.user != int64(a) || got.model != "cheap" || !got.started.Equal(started) || got.latency != 1500 ||
		got.prompt != 12 || got.completion != 3 || got.cost != cost || got.status != "ok" || got.errText != "" {
		t.Errorf("got %+v", got)
	}
	if got.request != `{"model": "cheap"}` || got.response != `{"choices": []}` {
		t.Errorf("got bodies %s and %s", got.request, got.response)
	}
}

func TestRecordFailedModelCallKeepsNulls(t *testing.T) {
	st, pool, a, _, ctxA, _ := twoScoped(t)
	conversation := active(t, st, ctxA, a)

	err := st.RecordModelCall(ctxA, a, store.ModelCall{
		ConversationID: conversation.ID,
		Model:          "cheap",
		StartedAt:      time.Now(),
		Latency:        30 * time.Second,
		Status:         "error",
		Error:          "context deadline exceeded",
		RequestJSON:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RecordModelCall: %v", err)
	}

	const nulls = `SELECT count(*) FROM model_calls WHERE cost_usd IS NULL AND response_json IS NULL AND error <> ''`
	if got := count(t, pool, nulls); got != 1 {
		t.Errorf("got %d rows with null cost and response, want 1", got)
	}
}

func TestRecordModelCallRoundsSubMillisecondLatencyUp(t *testing.T) {
	st, pool, a, _, ctxA, _ := twoScoped(t)
	conversation := active(t, st, ctxA, a)

	err := st.RecordModelCall(ctxA, a, store.ModelCall{
		ConversationID: conversation.ID,
		Model:          "cheap",
		StartedAt:      time.Now(),
		Latency:        200 * time.Microsecond,
		Status:         "ok",
		RequestJSON:    json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("RecordModelCall: %v", err)
	}

	if got := count(t, pool, `SELECT latency_ms FROM model_calls`); got != 1 {
		t.Errorf("got latency %d ms, want 1", got)
	}
}

func TestRecordModelCallOnOtherUsersConversationRejected(t *testing.T) {
	st, _, a, b, ctxA, ctxB := twoScoped(t)
	conversation := active(t, st, ctxA, a)

	err := st.RecordModelCall(ctxB, b, store.ModelCall{
		ConversationID: conversation.ID,
		Model:          "cheap",
		StartedAt:      time.Now(),
		Status:         "ok",
		RequestJSON:    json.RawMessage(`{}`),
	})

	if !errors.Is(err, store.ErrConversationNotFound) {
		t.Errorf("got error %v, want ErrConversationNotFound", err)
	}
}

func TestCostPerUserFromCallLog(t *testing.T) {
	st, pool, a, b, ctxA, ctxB := twoScoped(t)
	record := func(ctx context.Context, user identity.UserID, conversation int64, cost *float64) {
		t.Helper()
		err := st.RecordModelCall(ctx, user, store.ModelCall{
			ConversationID: conversation,
			Model:          "cheap",
			StartedAt:      time.Now(),
			CostUSD:        cost,
			Status:         "ok",
			RequestJSON:    json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("RecordModelCall: %v", err)
		}
	}
	price := func(v float64) *float64 { return &v }
	conversationA := active(t, st, ctxA, a).ID
	conversationB := active(t, st, ctxB, b).ID
	record(ctxA, a, conversationA, price(0.25))
	record(ctxA, a, conversationA, price(0.5))
	record(ctxA, a, conversationA, nil)
	record(ctxB, b, conversationB, price(2))

	rows, err := pool.Query(context.Background(),
		`SELECT user_id, sum(cost_usd)::float8 FROM model_calls GROUP BY user_id`)
	if err != nil {
		t.Fatalf("sum cost: %v", err)
	}
	got := make(map[identity.UserID]float64)
	var user int64
	var total float64
	if _, err := pgx.ForEachRow(rows, []any{&user, &total}, func() error {
		got[identity.UserID(user)] = total
		return nil
	}); err != nil {
		t.Fatalf("sum cost: %v", err)
	}

	if want := map[identity.UserID]float64{a: 0.75, b: 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
