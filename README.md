# Nestor

Nestor is a self-hosted personal assistant that one person talks to in plain language. It
manages tasks, appointments, reminders and email, gets better the more it is used, and never
takes a consequential action without explicit approval.

It is named after the unflappable, discreet butler of the Tintin stories (and the wise
counsellor of Greek myth). It is also a learning project: understanding how agents are built,
hosted and evaluated matters as much as the end result.

> **Status: early implementation.** Telegram long-polling chat, the agent loop (with local
> `TIMEZONE` clock context), PostgreSQL conversation persistence and model/tool call logging, the
> tool registry, inline approval cards (with conversational edits and stale-approval notices), and
> tasks and reminders (including a 30-second reminder scheduler) are implemented and running.
> Model routing, calendar, email, owner memory, proactive briefings, MCP servers, and skills are
> still planned — see the [Roadmap](#roadmap) below.

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

- **Agent:** a single static Go binary ([`cmd/nestor`](cmd/nestor/main.go)), with packages under `internal/`.
- **Models:** cloud providers only, reached through a LiteLLM proxy ([`deploy/litellm.yaml`](deploy/litellm.yaml)).
  Code uses the aliases `cheap` (Gemini Flash) and `strong` (Gemini 4 Hydrogen; swappable in
  LiteLLM config), never provider model IDs.
- **Storage:** PostgreSQL, with pgvector available for later memory work.
- **Tools:** a Go registry where every tool declares a risk level: `read`, `reversible` or
  `critical`. Built-in task (`task_add`, `task_list`, `task_complete`, `task_delete`) and
  reminder (`reminder_add`, `reminder_list`, `reminder_cancel`) tools are always enabled; demo
  tools (`current_time`, `notify_self`) are gated behind `ENABLE_DEMO_TOOLS`. Third-party tools
  can be added later as MCP servers.
- **Interface:** a Telegram bot using long polling, so there are no inbound ports. Remote
  admin goes through Tailscale.
- **Host:** an Intel N100 mini PC running Docker Compose, with the whole stack under about
  4 GB of RAM.

## Roadmap

Each step is one OpenSpec change, delivered as a thin vertical slice that runs end to end.

| # | Change | Scope | Status |
|---|--------|-------|--------|
| 1 | [`add-chat-foundation`](openspec/changes/archive/2026-11-14-add-chat-foundation/) | Telegram chat, agent loop, persistence, call logging | Done |
| 2 | [`add-tool-approval-gate`](openspec/changes/archive/2026-11-14-add-tool-approval-gate/) | Tool registry, risk levels, approve/reject cards, conversational edits ([`add-stale-approval-notices`](openspec/changes/archive/2026-11-15-add-stale-approval-notices/), [`refine-context-and-approvals`](openspec/changes/archive/2026-11-15-refine-context-and-approvals/)) | Done |
| 3 | [`add-tasks-and-reminders`](openspec/changes/archive/2026-11-15-add-tasks-and-reminders/) | First real tools (tasks, reminders) plus the 30s delivery scheduler | Done |
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
cmd/mcp-timezone/    Reference MCP server: read-only time zone tools
cmd/sandboxd/        Sidecar that runs skill scripts in throwaway containers with no network
config/system_prompt.md  Reviewed base persona and purpose system prompt (UTF-8, <= 4096 bytes)
config/mcp.yaml      MCP servers Nestor may use, with the risk of each allowlisted tool
config/skills.yaml   Reviewed skills, their pinned hashes and the users who get them
skills/              Skill folders (SKILL.md plus reference files), one per skill
internal/            Application packages
evals/               On-demand real-model evaluation suites (-tags eval)
deploy/              Docker Compose stack and LiteLLM configuration
docs/vision.md       Product intent, users, success criteria, roadmap, open questions
openspec/config.yaml Stack, constitution, code conventions and spec rules
openspec/specs/      Living specifications of implemented capabilities
openspec/changes/    Active and archived OpenSpec changes (proposal, design, specs, tasks)
```

## Running the stack

You need Docker with the Compose plugin, a Telegram bot token from
[@BotFather](https://t.me/BotFather), your numeric Telegram ID, and a Gemini API key (plus an
Anthropic API key only if you enable an Anthropic model in [`deploy/litellm.yaml`](deploy/litellm.yaml)).

```sh
cp deploy/.env.example deploy/.env   # then replace every value
docker compose -f deploy/docker-compose.yml up -d --build
docker compose -f deploy/docker-compose.yml --profile build build skill-runner   # image for skill scripts
```

Nestor applies database migrations and syncs the allowlist each time it starts. The
`skill-runner` image is never started by Compose: `sandboxd`, the only service given the Docker
socket, starts one throwaway container from it per skill script run, with no network.

### Environment variables

Set these in `deploy/.env`, which git ignores (validated by [`internal/config/config.go`](internal/config/config.go)).

| Variable | Required | Meaning |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | Yes | The bot's token |
| `ALLOWED_TELEGRAM_USERS` | Yes | Comma-separated `telegram_id:display_name` pairs. Only these people are served; removing one disables them and keeps their data |
| `POSTGRES_PASSWORD` | Yes | Database password, also used inside a URL, so keep it URL-safe |
| `LITELLM_MASTER_KEY` | Yes | Key Nestor uses to reach the LiteLLM proxy. Must start with `sk-` |
| `GEMINI_API_KEY` | Yes | Provider key for the `cheap` and `strong` aliases in `deploy/litellm.yaml`. Stays inside the LiteLLM container |
| `ANTHROPIC_API_KEY` | No | Optional provider key if an Anthropic model is enabled in `deploy/litellm.yaml`. Stays inside the LiteLLM container |
| `SYSTEM_PROMPT_FILE` | Yes (bot daemon) | Path to the reviewed UTF-8 system prompt file (`<= 4096` bytes). Set by `docker-compose.yml`, which mounts [`config/system_prompt.md`](config/system_prompt.md) read-only. CLI subcommands (`credentials`, `mcp`, `skills`) do not require it |
| `LLM_MODEL` | No | Model alias to use. Default `cheap` |
| `HISTORY_LIMIT` | No | Messages of the conversation sent to the model. Default `30` |
| `MAX_LOOP_ITERATIONS` | No | Model calls allowed for one message. Default `8` |
| `ENABLE_DEMO_TOOLS` | No | Register `current_time` and `notify_self` demo tools. Default `false` |
| `APPROVAL_TTL` | No | How long a proposed critical action waits for a tap before expiring. Default `24h` |
| `APPROVAL_STALE_AFTER` | No | How long an approved action may run before a sweep marks its outcome unknown and notifies the owner. Default `5m` |
| `TIMEZONE` | No | IANA timezone for local clock context, tasks and reminders. Default `Europe/Zurich` |
| `CREDENTIALS_KEY` | No | Base64 of 32 random bytes (`openssl rand -base64 32`) that encrypts per-user MCP credentials. Needed to store one with `nestor credentials set` |
| `MCP_CONFIG` | No | Path of the reviewed `mcp.yaml`. Set by `docker-compose.yml`, which mounts [`config/mcp.yaml`](config/mcp.yaml). Unset: no MCP servers |
| `SKILLS_CONFIG` | No | Path of the reviewed `skills.yaml`. Set by `docker-compose.yml`, which mounts [`config/skills.yaml`](config/skills.yaml). Unset: no skills |
| `SKILLS_DIR` | With `SKILLS_CONFIG` | Folder with one subfolder per skill. Set by `docker-compose.yml`, which mounts [`skills/`](skills/) read-only |
| `SANDBOXD_URL` | No | Where Nestor asks `sandboxd` to run skill scripts; must be an `http` URL to an internal host. Set by `docker-compose.yml` to `http://sandboxd:8080`. Unset: skill scripts are unavailable |
| `ADMIN_USER_ID` | No | Internal user ID (not a Telegram ID) who receives operational alerts, such as an MCP tool waiting for `nestor mcp approve`. Unset: alerts are only logged |

### Using the bot

Send it a text message to chat, manage tasks, or set reminders. Critical actions (`task_delete`,
`reminder_cancel`, and `notify_self` when `ENABLE_DEMO_TOOLS=true`) post an inline Telegram
approval card with **Approve** and **Reject** buttons; replying in chat to adjust the request
supersedes the earlier proposal with a fresh card. `/new` starts a fresh conversation. Other
message types get a short notice, and messages from anyone not on the allowlist are ignored and
logged.

### Querying the call and audit tables

Every model request is a row in `model_calls` (with its request and response bodies; a retried
request is two rows), tool dispatches are recorded in `tool_calls`, approval proposals and
outcomes live in `pending_actions`, and user items live in `tasks` and `reminders`. Open a
database shell with:

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
10. The logs show `mcp tool waiting for approval` for `get_time` and `convert_time`, and with
    `ADMIN_USER_ID` set you get a Telegram message for each. Ask "what time is it in Tokyo?".
    The reply may well be right: every request carries the clock and your time zone, and the
    demo `current_time` tool answers too when enabled. Judge by the tool calls, not the reply:
    `SELECT started_at, tool_name, status FROM tool_calls ORDER BY started_at DESC LIMIT 5;`
    (in the [database shell](#querying-the-call-and-audit-tables)) shows no `timezone__` row.
11. Approve both tools (see [MCP servers](#mcp-servers)). Within a minute the logs show
    `mcp tools enabled` with both names. Ask again. The same query now shows a
    `timezone__get_time` (or `timezone__convert_time`) row with status `executed`.
12. `docker compose -f deploy/docker-compose.yml stop mcp-timezone`, then ask "what time is it?".
    Nestor still answers (from the clock in its prompt), and the logs show
    `mcp server stopped answering`. Start it again and the tools come back without a restart.

### MCP servers

[`config/mcp.yaml`](config/mcp.yaml) lists the MCP servers Nestor connects to and the risk of each
tool it may use. Every tool stays disabled until you approve it, and is disabled again if its
description or input schema changes. Set `ADMIN_USER_ID` to your internal user ID
(`SELECT id, display_name FROM users;`) to get a Telegram message when a tool waits for approval.

```sh
docker compose -f deploy/docker-compose.yml exec nestor /nestor mcp approve timezone get_time
docker compose -f deploy/docker-compose.yml exec nestor /nestor mcp approve timezone convert_time
```

An approved tool is offered within one health check (about 30 seconds). For a server with
per-user credentials, store each user's token from stdin (it never appears in the shell history
or the logs):

```sh
docker compose -f deploy/docker-compose.yml exec -T nestor /nestor credentials set 1 todoist_token < token.txt
```

### MCP server isolation check

MCP server containers sit on the internal `mcp` network, which has no route out and which neither
Postgres nor LiteLLM joins. `go test ./deploy/` checks this in the Compose file. To check it on a
running stack, start a throwaway container on that network (the server images have no shell):

```sh
docker run --rm --network nestor_mcp busybox:1.36 sh -c '
for target in "mcp-timezone 8080" "postgres 5432" "1.1.1.1 443"; do
  set -- $target
  if nc -z -w 3 "$1" "$2" 2>/dev/null; then echo "REACHABLE   $1:$2"; else echo "unreachable $1:$2"; fi
done'
```

Expected: `mcp-timezone:8080` reachable, `postgres:5432` and `1.1.1.1:443` unreachable. Then
`docker inspect nestor-mcp-timezone-1 --format '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}}'`
prints `65532:65532 true`.

## Contributing

Nestor follows a spec-driven workflow with [OpenSpec](https://github.com/Fission-AI/OpenSpec).
Read `docs/vision.md` and `openspec/config.yaml` before proposing a change. Each change states
which constitution principles it touches, lists its non-goals, and has tasks that each name the
test that proves them.

## License

Licensed under the [Apache License 2.0](LICENSE).
