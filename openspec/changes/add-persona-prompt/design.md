# Design: Configurable persona and purpose system prompt

## Prompt loader (`internal/prompt`)

Create `internal/prompt/prompt.go` using Go standard library packages only:

- `const MaxBytes = 4096`
- `type Loaded struct { Text string; Bytes int; SHA256 string }`
- `LoadFile(path string) (Loaded, error)`:
  1. Trim `path`; return an error naming `SYSTEM_PROMPT_FILE` if empty.
  2. Open `path` and inspect `f.Stat()`. Reject missing/unreadable paths and non-regular files (`!info.Mode().IsRegular()`), preventing directories, FIFOs, or device nodes while supporting regular files and read-only single-file Docker bind mounts.
  3. Reject `info.Size() > MaxBytes` up front, then read at most `MaxBytes + 1` bytes using `io.LimitReader(f, MaxBytes+1)`. If `len(raw) > MaxBytes`, return an error stating the `4096`-byte limit without buffering oversized inputs.
  4. Reject embedded NUL bytes (`bytes.IndexByte(raw, 0) >= 0`) and invalid UTF-8 (`!utf8.Valid(raw)`).
  5. Trim leading and trailing whitespace (`strings.TrimSpace(string(raw))`); return an error if empty.
  6. Compute `sum := sha256.Sum256(raw)` over the raw file bytes (`Bytes: len(raw)`, `SHA256: "sha256:" + hex.EncodeToString(sum[:])`) so `sha256sum config/system_prompt.md` matches the logged digest.
- `RepoFile() (string, error)`:
  - Walks upward from `os.Getwd()` until a directory containing `go.mod` is found and returns `filepath.Join(dir, "config", "system_prompt.md")`.
  - Used by `evals/` (`LoadForEval()` or calling `LoadFile` on `os.Getenv("SYSTEM_PROMPT_FILE")` if non-empty, otherwise `RepoFile()`). Eval suites call `t.Fatalf` if loading fails and never fall back to a hardcoded prompt string.

## Configuration & startup wiring (`internal/config`, `cmd/nestor`)

- **`internal/config/config.go`:**
  - Add `SystemPromptPath string` to `Config`, populated via `get("SYSTEM_PROMPT_FILE")`.
  - Keep `config.Load` free of filesystem I/O and keep `SYSTEM_PROMPT_FILE` optional inside `config.Load` so administrative CLI subcommands (`nestor credentials`, `nestor mcp`, `nestor skills`) and existing minimal-env unit tests continue working without the prompt file mounted.
- **`cmd/nestor/main.go`:**
  - Remove `const systemPrompt`.
  - Add `loadSystemPrompt(cfg config.Config) (prompt.Loaded, error)` returning an error naming `SYSTEM_PROMPT_FILE` when `cfg.SystemPromptPath == ""`, otherwise delegating to `prompt.LoadFile(cfg.SystemPromptPath)`.
  - Call `loadSystemPrompt(cfg)` at the start of `run()` immediately after `config.Load(os.Getenv)`, before opening Postgres or initializing Telegram, skills, or MCP supervisors, so invalid prompt configuration exits non-zero immediately.
  - Log `slog.Info("system prompt loaded", "path", cfg.SystemPromptPath, "bytes", loaded.Bytes, "sha256", loaded.SHA256)` without logging `loaded.Text`.
  - Update `agentOptions(cfg config.Config, systemPrompt string, reg *tools.Registry, loc *time.Location) agent.Options` to accept the loaded `systemPrompt` string.
- **`internal/agent/agent.go` and `internal/websearch/websearch.go`:**
  - Unchanged. `Agent.systemPrompt` continues placing `Options.SystemPrompt` first ahead of `Current time:` and `Skills.PromptBlock(user)`, preserving prompt-prefix stability across turns. `internal/websearch`'s `baseSystemPrompt` remains independent.

## Authoring structure of `config/system_prompt.md`

Create `config/system_prompt.md` as plain UTF-8 Markdown (~1–2 KiB, well below the `4096`-byte limit), containing zero HTML/reviewer comments, zero user-identifying facts, and zero hardcoded tool inventories or `HISTORY_LIMIT` numbers:

1. **Identity & communication style:** French personality expressed through direct, concise answers and a mild disregard for authority—never opening with sycophantic filler (such as *"You're absolutely right"*, *"Vous avez tout à fait raison"*, or similar openers in any language). Polite, respectful, and genuinely caring without being obsequious. Always reply in the language of the current conversation, using clean plain-text formatting suited for Telegram (avoiding noisy Markdown headers or excessive markup).
2. **Humor & operational mode boundaries:** Allow occasional dry, friendly humor—including light, affectionate teasing of the user—during casual conversation. Drop humor completely and keep replies brief, neutral, and straight to the point when executing tasks, confirming actions, proposing critical actions for approval, or reporting tool/system errors.
3. **Purpose & honest capability limits:** Help the user stay organized so tasks, reminders, and commitments do not get ignored. Act strictly through the tools and skills provided in the current conversation; never claim integrations or capabilities absent from the turn's tools. Never claim a pending critical action has already executed, and never instruct the user to approve by typing text (approval uses the inline Telegram card buttons). State plainly that only explicitly saved items (tasks and one-shot reminders) persist across conversations—offering to create a task or reminder when helpful rather than promising long-term memory about people/facts or recurring proactive briefings.

## Deployment & evals wiring (`deploy/`, `evals/`, `README.md`)

- **`deploy/docker-compose.yml`:** Add `SYSTEM_PROMPT_FILE: /etc/nestor/system_prompt.md` under `services.nestor.environment` and `- ../config/system_prompt.md:/etc/nestor/system_prompt.md:ro` under `services.nestor.volumes`. No other service receives `SYSTEM_PROMPT_FILE` or mounts `system_prompt.md`.
- **`deploy/.env.example` and `README.md`:** Document `SYSTEM_PROMPT_FILE` (configured by Compose; set when running the binary directly outside Compose).
- **`evals/{approval,mcp,skills,tasks}`:** Replace the duplicated `"You are Nestor, a helpful personal assistant. Be concise."` literal in each suite's `newRig` with `internal/prompt` loading `config/system_prompt.md`. Update `evals/` baseline checks where needed so a prompt change causing refusal of benign/bait tools (`fetch_note`, `evil__get_weather`, `notify_self`) fails loudly instead of silently skipping.

## Failure modes

| Failure mode | Behaviour |
|---|---|
| `SYSTEM_PROMPT_FILE` unset in `run()` | `run()` exits non-zero before DB/Telegram startup, logging an error naming `SYSTEM_PROMPT_FILE` with no secret values. CLI subcommands (`credentials`, `mcp`, `skills`) are unaffected. |
| Prompt file missing, directory, unreadable, empty/whitespace, NUL byte, invalid UTF-8, or `> 4096` bytes | `prompt.LoadFile` returns a descriptive error without buffering large files; `run()` exits non-zero before serving any message and never starts with a blank system prompt. |
| Host edits `config/system_prompt.md` while running | Running `nestor` keeps the startup-loaded prompt until restarted (consistent with `mcp.yaml` and `skills.yaml`), keeping `model_calls` deterministic per process run. |
| Existing open conversation across restart | Prior `assistant` turns remain in Postgres `messages` until `/new` or history rollover; no DB migration is required and rollback to a previous image takes effect immediately. |
| LiteLLM / model provider down (5xx / timeout) | Unchanged `agent-loop` retry-once-on-5xx and `noticeModelFailure`, recorded in `model_calls`. |
| Postgres down | Startup `postgres.Open` fails fast; mid-run store errors abort the turn fail-closed as today. |
| Telegram down | Unchanged polling backoff and retry of unsent approval cards/outcome notices without leaking prompt or message text to logs. |

## Dependencies

- **Zero new external Go dependencies.** Uses Go standard library packages only (`bytes`, `crypto/sha256`, `encoding/hex`, `errors`, `fmt`, `io`, `os`, `path/filepath`, `strings`, `unicode/utf8`).

## Testing approach

- **`internal/prompt/prompt_test.go`:** Table-driven tests covering valid load + trimming + raw byte count + `sha256` digest, empty `path`, missing file, directory rejection, empty/whitespace-only rejection, NUL byte rejection, invalid UTF-8 rejection, `4096`-byte boundary (`4096` passes, `4097` fails), `RepoFile()` loading the repository's `config/system_prompt.md`, and verifying `config/system_prompt.md` contains no HTML comments (`<!--`).
- **`internal/config/config_test.go`:** Verify `SYSTEM_PROMPT_FILE` is trimmed into `SystemPromptPath` when set and remains optional in `config.Load` when unset.
- **`cmd/nestor/main_test.go`:** Subprocess/unit tests verifying `run()` exits non-zero naming `SYSTEM_PROMPT_FILE` when unset or pointing to an invalid file without leaking secrets, `loadSystemPrompt` + `agentOptions` pass loaded prompt text, startup logs emit `path`, `bytes`, and `sha256` without logging prompt contents, and `credentials`/`mcp`/`skills` CLI subcommands still run without `SYSTEM_PROMPT_FILE`.
- **`deploy/compose_test.go`:** Verify `nestor` sets `SYSTEM_PROMPT_FILE` and mounts `../config/system_prompt.md:/etc/nestor/system_prompt.md:ro`, and no other service mounts `system_prompt.md`.
- **`evals/`:** Verify all four suites compile and type-check (`go test -tags eval -run=^$ ./evals/...`); run live `-tags eval` only when explicitly requested by the owner, while the owner checks conversational tone and persona directly in chat.
