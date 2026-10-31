# Nestor

Nestor is a self-hosted personal assistant that one person talks to in plain language. It
manages tasks, appointments, reminders and email, gets better the more it is used, and never
takes a consequential action without explicit approval.

It is named after the unflappable, discreet butler of the Tintin stories (and the wise
counsellor of Greek myth). It is also a learning project: understanding how agents are built,
hosted and evaluated matters as much as the end result.

> **Status: design phase.** There is no code to run yet. This repository currently holds the
> product vision and the OpenSpec changes for the first pieces of the system. Everything below
> describes the intended design, not working software.

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
| 1 | [`add-chat-foundation`](openspec/changes/add-chat-foundation/) | Telegram chat, agent loop, persistence, call logging | Specified |
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

Changes 9 and 10 depend only on change 2 and can be pulled earlier. No change is implemented yet.

## Repository layout

```
docs/vision.md       Product intent, users, success criteria, roadmap, open questions
openspec/config.yaml Stack, constitution, code conventions and spec rules
openspec/changes/    One folder per change: proposal, design, specs and tasks
```

## Contributing

Nestor follows a spec-driven workflow with [OpenSpec](https://github.com/Fission-AI/OpenSpec).
Read `docs/vision.md` and `openspec/config.yaml` before proposing a change. Each change states
which constitution principles it touches, lists its non-goals, and has tasks that each name the
test that proves them.

## License

Licensed under the [Apache License 2.0](LICENSE).
