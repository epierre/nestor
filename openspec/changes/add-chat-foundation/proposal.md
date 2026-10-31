# Add chat foundation

## Why

Nothing exists yet. Before adding tools, memory or routing, the project needs the thinnest
slice that runs end to end on the N100: a user sends a Telegram message, the agent asks
a model, the user gets a reply, and every step is recorded. Every later change builds on
this skeleton, so its seams (channel, model client, store, call log) must be right.

## What changes

- A `users` table and an identity mapping from Telegram user IDs to user IDs, seeded from
  an allowlist in configuration (one entry in v1).
- A Telegram channel that accepts messages only from allowlisted identities, resolves each
  to a user ID, and sends replies.
- An agent loop that builds a prompt from the system prompt plus recent conversation
  history, calls a model, and returns the reply. Tool calling is wired as an extension
  point but no tools are registered yet.
- A model client that talks to an OpenAI-compatible endpoint (the LiteLLM proxy), with
  the model name taken from configuration.
- Conversation persistence in Postgres, every row owned by a user ID.
- A call log recording every model call (user, latency, tokens, cost estimate, status).
- Docker Compose stack: nestor, Postgres, LiteLLM proxy.
- `/new` command to start a fresh conversation.

## Capabilities

- `chat-interface` (new)
- `agent-loop` (new)
- `observability` (new)

## Constitution principles touched

- 3 Provider-agnostic: the agent only knows an OpenAI-compatible endpoint and a model name.
- 5 Observable: every model call lands in the call log.
- 6 User-scoped: identity is resolved at the channel; conversations, messages and call log
  rows carry a user ID and every store query filters by it; non-allowlisted senders are
  ignored and logged.
- 7 Secrets: bot token and proxy key come from environment variables.

## Non-goals

- Tools, approvals, reminders (next changes).
- Model tiers, fallback between providers, budgets (`add-model-routing`).
- Long-term memory about users.
- Streaming partial replies, voice messages, images or files.
- A web UI.
- Adding users at runtime, signup, admin commands, roles, per-user model choice or budgets.

## Impact

- New repository layout: `cmd/nestor`, `internal/{channel,agent,model,store,obs,config}`,
  `migrations/`, `deploy/docker-compose.yml`, `deploy/litellm.yaml`.
- New external services: Telegram Bot API, the LiteLLM proxy, at least one model provider key.
