## 1. Skeleton that runs

- [x] 1.1 Create Go module, `cmd/nestor/main.go`, `internal/config` loading env vars with validation, including parsing `ALLOWED_TELEGRAM_USERS`. Test: `config_test.go` covers missing required variable, malformed allowlist entry, and no secret in error text.
- [x] 1.2 Add `deploy/docker-compose.yml` (nestor, postgres, litellm) and `deploy/litellm.yaml` with aliases `cheap` (Gemini Flash) and `strong` (Claude Opus 5.5). Test: `docker compose config` succeeds in CI; manual smoke noted in README.
- [x] 1.3 Define interfaces `Channel`, `ModelClient`, `Store` and the `Agent` struct with fakes in `internal/testutil`; add `internal/identity` with `WithUser`/`UserFrom` context helpers. Test: `agent_test.go` "replies with fake model output", `identity_test.go` "missing user returns error".

## 2. Model client

- [x] 2.1 Implement OpenAI-compatible chat completions client (messages, tools, usage, cost header/field). Test: `client_test.go` against `httptest` server replaying recorded responses.
- [x] 2.2 Timeout and single retry on 5xx only. Test: `client_test.go` "503 retried once", "400 not retried", "timeout returns error".

## 3. Persistence

- [x] 3.1 First migration: users, user_identities, conversations, messages, model_calls, with user_id on every user-data table and composite FKs keeping child rows in their parent's user. Test: integration `store_test.go` "migrate up and down", "message with mismatched user and conversation rejected".
- [ ] 3.2 Allowlist sync on startup: upsert users and identities, disable removed identities. Test: integration `store_test.go` "allowlist upsert idempotent", "removed identity disabled, data kept".
- [ ] 3.3 Store methods, all taking a user ID: active conversation, append message, recent history (limit), close conversation, record model call. Test: integration `store_test.go` per method with two seeded users, asserting ordering, limit, and that user B's data never appears in user A's results; "unscoped call returns error".

## 4. Agent loop

- [ ] 4.1 Build request from system prompt + the user's history; persist user and assistant messages. Test: `agent_test.go` "history within limit", "history beyond limit", "restart keeps context" (fake store with persistence), "two users never see each other's messages".
- [ ] 4.2 Bounded iterations and failure notices. Test: `agent_test.go` "runaway loop stops at max", "model error yields notice".
- [ ] 4.3 Record every model call with redacted request. Test: `agent_test.go` "call recorded on success and failure", "no Authorization in stored request".

## 5. Telegram channel

- [ ] 5.1 Long-poll client with offset advanced only after handling, backoff on failure. Test: `telegram_test.go` against fake Bot API server, "update re-delivered after crash", "backoff on 502".
- [ ] 5.2 Identity resolution (Telegram ID to user ID, drop unknown), non-text notice, `/new` command, message splitting. Test: `telegram_test.go` "stranger ignored", "disabled identity ignored", "agent receives internal user ID only", "voice message notice", "split 9000 chars", plus `agent_test.go` "/new clears only the sender's history".

## 6. Observability and wrap-up

- [ ] 6.1 slog JSON handler with request ID and user ID propagation via context. Test: `obs_test.go` "log lines share request ID and user ID".
- [ ] 6.2 README: setup, env vars, how to query the call log. Test: manual smoke checklist in README executed once on the N100.
- [ ] 6.3 Run `openspec validate add-chat-foundation --strict` and `go test ./...` green.
