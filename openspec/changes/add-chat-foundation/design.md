# Design: chat foundation

## Overview

```
Telegram ──long poll──▶ channel ──▶ agent.HandleMessage ──▶ model.Client ──▶ LiteLLM ──▶ provider
                          ▲                │    ▲
                          └── reply ───────┘    └── store (conversations, messages, model_calls)
```

One process (`cmd/nestor`) wires four interfaces together:

```go
type Channel interface {             // internal/channel
    Run(ctx context.Context, h Handler) error
    Send(ctx context.Context, chatID int64, text string) error
}

type ModelClient interface {         // internal/model
    Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

type Store interface { ... }         // internal/store: conversations, messages, model_calls

type Agent struct { ... }            // internal/agent: the loop
```

## Identity

The channel is the only place that knows about Telegram identities. For each update it looks
up `user_identities(platform='telegram', external_id=<sender id>)`. No match: log and drop.
Match: put the `user_id` into the request context (`identity.WithUser(ctx, id)`) and call the
agent. The agent and store read the user ID from context and pass it explicitly to every
store method; a store method called without a user ID returns an error rather than querying
unscoped.

On startup, `ALLOWED_TELEGRAM_USERS` (comma-separated `telegram_id:display_name` pairs) is
upserted into `users` and `user_identities`. Removing an entry disables that identity
(`disabled_at` set) but keeps the user's data. Adding a second user later is a config change.

`CompletionRequest` carries messages, an optional tool list, the model name and limits.
`CompletionResponse` carries either text or tool calls, plus usage. Tool calls are part of the
types now so the next change adds behaviour without reshaping the interface.

## Agent loop

1. Load the active conversation for the user in context (create one if none).
2. Append the user message; load the last N messages (config, default 30) as history.
3. Call the model with the system prompt + history.
4. If the response contains tool calls and no tools are registered, record it as an error
   and reply with a fallback message (cannot happen in practice yet; guards the extension point).
5. Persist the assistant message, send it, done.

The loop has a hard cap on iterations (config, default 8) so a later tool-calling bug cannot
loop forever or burn money.

## Data model (first migration)

- `users(id, display_name, created_at, disabled_at)`
- `user_identities(platform, external_id, user_id, chat_id, disabled_at)`; primary key (platform, external_id)
- `conversations(id, user_id, started_at, ended_at)`; at most one open conversation per user
- `messages(id, user_id, conversation_id, role, content, created_at)`; role in user/assistant/system/tool
- `model_calls(id, user_id, conversation_id, model, started_at, latency_ms, prompt_tokens,
   completion_tokens, cost_usd, status, error, request_json, response_json)`

`user_id` is denormalised onto `messages` and `model_calls` so every query can filter by it
directly, and per-user cost totals are a single `GROUP BY`. Foreign keys from child rows check
that `conversation_id` belongs to the same `user_id` (composite FK on `(id, user_id)`).

`request_json`/`response_json` are kept in full for now (small volume); a later change can add
retention.

## Dependencies

- `github.com/jackc/pgx/v5`: Postgres driver, de facto standard for Go.
- `github.com/pressly/goose/v3` (or embedded SQL with a tiny runner): migrations.
- `github.com/openai/openai-go` or a hand-written HTTP client: talking to the OpenAI-compatible
  proxy. A hand-written client is ~150 lines and teaches the protocol; pick it unless tool-call
  parsing gets painful.
- Telegram: `github.com/go-telegram/bot` or a hand-written long-poll client over the Bot API.
- LiteLLM proxy container (Python, but isolated behind HTTP): gives one API for Claude, Gemini,
  OpenAI, Kimi and others, plus per-call cost data.

## Configuration

Environment variables: `TELEGRAM_BOT_TOKEN`, `ALLOWED_TELEGRAM_USERS`, `DATABASE_URL`,
`LLM_BASE_URL`, `LLM_API_KEY`, `LLM_MODEL`, `HISTORY_LIMIT`, `MAX_LOOP_ITERATIONS`.
The process refuses to start if a required variable is missing.

`deploy/litellm.yaml` defines two model aliases from day one, `cheap` (Gemini Flash) and
`strong` (Claude Opus 5.5), each mapped to the provider's model ID and API key. The agent
only ever sends an alias; which provider model sits behind it is LiteLLM configuration.
This change uses a single model, `LLM_MODEL=cheap` by default, to keep development cheap;
`add-model-routing` later chooses between the aliases per task.

## Failure modes

- Model provider / proxy down or times out (30 s default): call logged with status `error`,
  the user receives "I couldn't reach the model, try again shortly." No retry storm; one retry
  with backoff for 5xx only.
- Postgres down at startup: process exits non-zero and Docker restarts it. Down mid-request:
  the user gets an error reply if Telegram is reachable; the message is not lost on Telegram's side
  because the update offset is only advanced after handling.
- Telegram unreachable: long-poll loop retries with exponential backoff (max 60 s), logs each failure.

## Testing approach

- Unit: agent loop against a fake `ModelClient` and in-memory/fake store.
- Integration (`integration` tag): store against a Postgres container, always with two seeded
  users so that every test also checks the other user's data is invisible.
- Contract: model client against a local HTTP test server replaying recorded proxy responses.
- Manual smoke: `docker compose up`, message the bot, check the `model_calls` row.
