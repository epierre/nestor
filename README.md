# Nestor

Nestor is a self-hosted personal assistant that one person talks to in plain language. It
manages tasks, appointments, reminders and email, gets better the more it is used, and never
takes a consequential action without explicit approval.

It is named after the unflappable, discreet butler of the Tintin stories (and the wise
counsellor of Greek myth). It is also a learning project: understanding how agents are built,
hosted and evaluated matters as much as the end result.

> **Status: early implementation.** The Compose stack starts, but Nestor cannot chat yet: it
> only validates its configuration and waits. This repository mainly holds the product vision
> and the OpenSpec changes for the first pieces of the system. Unless a section says otherwise,
> everything below describes the intended design, not working software.

## What it will do

- **Chat from your phone:** message Nestor on Telegram and get a reply within seconds.
- **Tasks and reminders:** add, list and complete tasks, and get messaged at the right time.
- **Calendar:** ask what's coming up, and confirm before events are created or moved.
- **Email:** get summaries and draft replies, with nothing sent until you approve it.
- **Memory:** learn stable facts and preferences, which you can review, correct or delete.
- **Accountability:** see what the assistant did, when, and what it cost.

v1 serves one person. The design leaves room for a few trusted people on the same host (a
household), with their data, approvals, credentials and costs fully separate. A public service
is not a goal. See [the vision](docs/vision.md) for the full scope and what is out of scope.

## Design principles

The full constitution is in [`openspec/config.yaml`](openspec/config.yaml). Every change must
respect it. The principles that shape the system most:

- **Approval is enforced in code, not in prompts.** A `critical` tool (send email, delete data,
  accept an invitation) runs only after an approval recorded in the database. That approval can
  only come from the user's own tap on a Telegram button, bound to the exact arguments shown.
- **Tool content is untrusted data.** Emails, calendar entries and files can never grant
  approval, widen permissions or change configuration.
- **Personal data stays on the host.** Only model requests leave the box, and they carry only
  what the current turn needs.
- **Everything is observable.** Every model call and tool call is recorded with inputs, outputs,
  latency, token counts and estimated cost.
- **Provider-agnostic.** Swapping a model is a config change, not a code change.
- **User-scoped by design.** Every piece of user data belongs to exactly one user ID, and only
  allowlisted identities are served.

## Architecture

```
Telegram ──long poll──▶ channel ──▶ agent ──▶ model client ──▶ LiteLLM ──▶ provider
                          ▲          │  │
                          └── reply ─┘  ├──▶ store (PostgreSQL)
                                        └──▶ tool registry ──▶ approval gate
```

- **Agent:** a single static Go binary (`cmd/nestor`), with packages under `internal/`.
- **Models:** cloud providers only, reached through a LiteLLM proxy. Code uses the aliases
  `cheap` (Gemini Flash) and `strong` (Claude Opus 5.5), never provider model IDs.
- **Storage:** PostgreSQL, with pgvector available for later memory work.
- **Tools:** a Go registry where every tool declares a risk level: `read`, `reversible` or
  `critical`. Third-party tools can be added later as MCP servers.
- **Interface:** a Telegram bot using long polling, so there are no inbound ports. Remote
  admin goes through Tailscale.
- **Host:** an Intel N100 mini PC running Docker Compose, with the whole stack under about
  4 GB of RAM.

## Roadmap

Each step is one OpenSpec change, delivered as a thin vertical slice that runs end to end.

| # | Change | Scope | Status |
|---|--------|-------|--------|
| 1 | [`add-chat-foundation`](openspec/changes/add-chat-foundation/) | Telegram chat, agent loop, persistence, call logging | In progress |
| 2 | [`add-tool-approval-gate`](openspec/changes/add-tool-approval-gate/) | Tool registry, risk levels, approve/edit/reject flow | Specified |
| 3 | `add-tasks-and-reminders` | First real tools plus the scheduler | Planned |
| 4 | `add-model-routing` | Model tiers, fallbacks, cost tracking, budget alerts | Planned |
| 5 | `add-calendar` | Read the calendar, propose changes through the gate | Planned |
| 6 | `add-email` | Summaries, drafts, sending via approval | Planned |
| 7 | `add-owner-memory` | Profile facts, episodic recall, review/correct/forget | Planned |
| 8 | `add-proactive-briefings` | Morning summary, important-mail alerts | Planned |
| 9 | [`add-mcp-servers`](openspec/changes/add-mcp-servers/) | Third-party MCP tools with configured risk levels | Specified |
| 10 | [`add-skills`](openspec/changes/add-skills/) | Reviewed, pinned skills, optional sandboxed scripts | Specified |
| 11 | `add-household-users` | (Later, optional) further users, per-user budgets | Planned |

Changes 9 and 10 depend only on change 2 and can be pulled earlier.

## Repository layout

```
cmd/nestor/          Entrypoint
internal/            Application packages
deploy/              Docker Compose stack and LiteLLM configuration
docs/vision.md       Product intent, users, success criteria, roadmap, open questions
openspec/config.yaml Stack, constitution, code conventions and spec rules
openspec/changes/    One folder per change: proposal, design, specs and tasks
```

## Running the stack

You need Docker with the Compose plugin, a Telegram bot token from
[@BotFather](https://t.me/BotFather), your numeric Telegram ID, and Gemini and Anthropic API keys.

```sh
cp deploy/.env.example deploy/.env   # then replace every value
docker compose -f deploy/docker-compose.yml up -d --build
```

Nestor applies database migrations and syncs the allowlist each time it starts.

### Environment variables

Set these in `deploy/.env`, which git ignores.

| Variable | Required | Meaning |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | Yes | The bot's token |
| `ALLOWED_TELEGRAM_USERS` | Yes | Comma-separated `telegram_id:display_name` pairs. Only these people are served; removing one disables them and keeps their data |
| `POSTGRES_PASSWORD` | Yes | Database password, also used inside a URL, so keep it URL-safe |
| `LITELLM_MASTER_KEY` | Yes | Key Nestor uses to reach the LiteLLM proxy. Must start with `sk-` |
| `GEMINI_API_KEY` | Yes | Provider key for the `cheap` alias. Stays inside the LiteLLM container |
| `ANTHROPIC_API_KEY` | Yes | Provider key for the `strong` alias. Stays inside the LiteLLM container |
| `LLM_MODEL` | No | Model alias to use. Default `cheap` |
| `HISTORY_LIMIT` | No | Messages of the conversation sent to the model. Default 30 |
| `MAX_LOOP_ITERATIONS` | No | Model calls allowed for one message. Default 8 |

### Using the bot

Send it a text message. `/new` starts a fresh conversation. Other message types get a short
notice, and messages from anyone not on the allowlist are ignored and logged.

### Querying the call log

Every model request is a row in `model_calls`, with its request and response bodies. A retried
request is two rows. Open a database shell with:

```sh
docker compose -f deploy/docker-compose.yml exec postgres psql -U nestor nestor
```

The latest calls:

```sql
SELECT started_at, user_id, model, status, latency_ms, prompt_tokens, completion_tokens, cost_usd, error
FROM model_calls ORDER BY started_at DESC LIMIT 20;
```

What each user has spent. `cost_usd` is null when the proxy reports no cost, so this total can be
an underestimate; the last column shows how many calls it leaves out:

```sql
SELECT u.display_name, count(*) AS calls, sum(c.cost_usd) AS cost_usd, count(*) - count(c.cost_usd) AS uncosted
FROM model_calls c JOIN users u ON u.id = c.user_id
GROUP BY u.display_name ORDER BY cost_usd DESC NULLS LAST;
```

Why a call failed:

```sql
SELECT started_at, error, response_json FROM model_calls
WHERE status = 'error' ORDER BY started_at DESC LIMIT 5;
```

### Reading the logs

Logs are JSON lines. All lines for one message share a `request_id` and `user_id`, and they never
contain message text. To follow one message:

```sh
docker compose -f deploy/docker-compose.yml logs nestor | grep '"request_id":"THE_ID"'
```

### Manual smoke test

1. `docker compose -f deploy/docker-compose.yml ps` shows `postgres` and `litellm` as healthy
   and `nestor` as running.
2. `docker compose -f deploy/docker-compose.yml logs nestor` shows `nestor started`, with no
   secret in the output and no `telegram poll failed` lines.
3. From an allowlisted account, send the bot "hello" and get a reply. The logs show
   `message received`, `model call finished` and `reply sent` with the same `request_id`.
4. The first query above shows one new row with status `ok`, non-zero latency and token counts.
   `cost_usd` should be filled in; if it is null, the proxy reported no cost for that model.
5. Send "my code word is falcon", then "what is my code word?". The reply knows it.
6. Restart with `docker compose -f deploy/docker-compose.yml restart nestor` and ask again. It
   still knows.
7. Send `/new`, then ask again. It no longer knows.
8. Send a voice message. You get the text-only notice and no new `model_calls` row.
9. From an account not on the allowlist, message the bot. Nothing comes back, and the logs show
   `ignored message from unknown sender`.

## Contributing

Nestor follows a spec-driven workflow with [OpenSpec](https://github.com/Fission-AI/OpenSpec).
Read `docs/vision.md` and `openspec/config.yaml` before proposing a change. Each change states
which constitution principles it touches, lists its non-goals, and has tasks that each name the
test that proves them.

## License

Licensed under the [Apache License 2.0](LICENSE).
