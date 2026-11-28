# Add configurable persona and purpose system prompt

## Why

Nestor currently hardcodes a 12-token placeholder prompt (`"You are Nestor, a helpful personal assistant. Be concise."`) in `cmd/nestor/main.go`, duplicated across all four real-model evaluation suites under `evals/`. This leaves Nestor with flat, sycophantic default model phrasing, no explicit statement of his organizational purpose or persistence limits, and requires rebuilding the Go binary and container image for every wording change. Moving the base prompt into a reviewed `config/system_prompt.md` file gives Nestor a consistent, respectful, non-sycophantic persona and honest capability boundaries while letting operators iterate on wording through configuration.

## What changes

- **Shared prompt loader (`internal/prompt`):** Add a small standard-library-only package providing `MaxBytes = 4096`, `Loaded{Text, Bytes, SHA256}`, `LoadFile(path)`, and a repo-root lookup helper (`RepoFile()`) for `evals/`. Validate that the file is a regular file, at most `4096` raw bytes (using a bounded reader), valid UTF-8 with no NUL bytes, and non-empty after `strings.TrimSpace`, computing a `sha256:<hex>` digest over the raw file bytes.
- **Configuration & daemon startup (`internal/config`, `cmd/nestor`):** Add `SystemPromptPath string` to `config.Config`, parsed from `SYSTEM_PROMPT_FILE` in `config.Load` with zero filesystem I/O so administrative CLI subcommands (`nestor credentials`, `nestor mcp`, `nestor skills`) continue to work without the prompt file mounted. In `run()` (`cmd/nestor/main.go`), require `SYSTEM_PROMPT_FILE`, load and validate it before opening Postgres or starting polling (exiting non-zero on error), emit a content-free structured startup log (`"system prompt loaded"` with `path`, `bytes`, and `sha256`), and pass `Loaded.Text` to `agent.Options.SystemPrompt` (removing `const systemPrompt`).
- **Reviewed persona & purpose prompt (`config/system_prompt.md`):** Host-wide, user-agnostic plain Markdown file (authoring target ~1–2 KiB, hard cap `4096` bytes, no inline reviewer or HTML comments since every byte reaches the model) defining:
  1. *Persona:* Direct French manner with a mild disregard for authority (never opening with sycophantic filler such as `"You're absolutely right"` or equivalents in other languages), good manners and genuine care without obsequiousness, replying in the language of the conversation, and occasional dry, friendly humor (including light, respectful teasing in casual chat)—while dropping humor completely to stay brief, plain-text, and factual when executing tasks, proposing critical approvals, confirming actions, or reporting errors.
  2. *Purpose & honest boundaries:* Keeping the user organized so tasks, reminders, and commitments do not slip through the cracks; acting strictly through the tools and skills provided in the active turn (never hardcoding tool lists or claiming unavailable integrations); stating plainly that only saved items (tasks and one-shot reminders) persist across conversations (offering those native tools instead of promising permanent people/fact memory or recurring briefings); and never claiming a pending critical action has already run or asking the user to approve via chat text instead of the Telegram card.
- **Deployment, evals & docs (`deploy/`, `evals/`, `README.md`):** Mount `../config/system_prompt.md:/etc/nestor/system_prompt.md:ro` and set `SYSTEM_PROMPT_FILE: /etc/nestor/system_prompt.md` on the `nestor` service only in `deploy/docker-compose.yml` (enforced by `deploy/compose_test.go`), document `SYSTEM_PROMPT_FILE` in `deploy/.env.example` and `README.md`, and switch all four existing real-model suites (`evals/approval`, `evals/mcp`, `evals/skills`, `evals/tasks`) from hardcoded prompt strings to `internal/prompt`.

## Capabilities

- `agent-loop` (modified)

## Constitution principles touched

- **1. Approval is enforced in code:** Critical tools remain gated exclusively in `internal/approval`; `config/system_prompt.md` instructs clear reply UX around approval cards and never serves as an enforcement boundary.
- **2. Content from tools is untrusted data:** `config/system_prompt.md` is operator-reviewed static configuration mounted `:ro`; tool outputs remain isolated in `[data]`-marked `RoleTool` messages.
- **3. Provider-agnostic:** Plain UTF-8 Markdown sent as standard `RoleSystem` content through LiteLLM aliases (`cheap`, `strong`), adding no vendor SDKs or provider-specific parameters.
- **4. Personal data stays on the host:** `config/system_prompt.md` is strictly user-agnostic and contains zero personal data, names, or secrets.
- **5. Everything is observable:** Startup logs record `path`, `bytes`, and `sha256` without logging prompt text; every turn's full `RequestJSON` continues to be recorded in `model_calls`.
- **6. User-scoped by design:** The host-wide base persona contains no single-user assumptions, while `Agent.systemPrompt(user)` preserves per-user skill filtering and user-isolated conversation history.
- **7. Secrets never live in the repo:** `config/system_prompt.md` holds non-secret instructions only; startup error messages name `SYSTEM_PROMPT_FILE` and the configured path without exposing any secrets.
- **8. Small, reversible steps:** Thin vertical slice across config loading, startup validation, Compose wiring, and existing eval suites, leaving `internal/agent/agent.go` unchanged, with zero new external dependencies and zero database migrations.

## Non-goals

- Persistent memory about people, birthdays, or profile facts (`add-owner-memory`) and recurring/proactive briefings (`add-proactive-briefings`).
- Per-user custom prompts, database-stored prompt overrides, or hot-reloading without restarting `nestor`.
- Modifying `internal/agent/agent.go` prompt assembly (no `HISTORY_LIMIT` injection and no loop-level tone overrides).
- Changing `internal/websearch`'s isolated `baseSystemPrompt` or altering deterministic Go fallback/approval/scheduler notices (`noticeModelFailure`, `noticeLoopLimit`, `noticeNewConversation`, Telegram card/callback strings).
- Asserting exact model wording in OpenSpec scenarios or unit tests, or adding new automated persona-grading suites under `evals/` (the owner verifies persona and tone directly in chat).

## Impact

- Packages/files to add or modify: `internal/prompt` (new), `config/system_prompt.md` (new), `internal/config`, `cmd/nestor`, `deploy/docker-compose.yml`, `deploy/.env.example`, `deploy/compose_test.go`, `evals/approval`, `evals/mcp`, `evals/skills`, `evals/tasks`, `README.md`.
- New environment variable: `SYSTEM_PROMPT_FILE` (set in `deploy/docker-compose.yml` to `/etc/nestor/system_prompt.md`; required by the bot daemon, optional/unused by CLI subcommands).
- Zero new external Go dependencies, zero database migrations.
