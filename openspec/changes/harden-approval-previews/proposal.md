# Harden approval previews

## Why

Nestor enforces that no `critical` tool call executes until its owner taps Approve on a Telegram card showing what will run. Today that guarantee has four connected defects (tracking `nestor-lgd` and the `action-approval` / `mcp-integration` drift in `2026-11-23_DRIFT_REPORT.md`):

1. **Blind MCP approvals (`nestor-lgd`):** `adapterTool.Preview` in `internal/mcp/adapter.go` returns only `"MCP tool <server>__<tool>"` and hides all call arguments, leaving the user unable to see what a critical MCP call will do.
2. **Unbounded previews cause permanent delivery failures:** Nothing caps preview length before storing a pending action. Any critical preview whose card or outcome notice exceeds Telegram's 4096 UTF-16 code unit limit fails `SendApprovalCard` or `Notify` permanently and retries on every user message. For `OutcomeNoticeText` (which has no expiry TTL and adds 85 UTF-16 units), an oversized stored preview loops forever.
3. **Parser differentials and visual spoofing:** `tools.Registry.Validate` skips validation when a tool has a `nil` schema and relies on Go's standard JSON unmarshaler, which silently applies last-key-wins on duplicate keys—allowing a preview to disagree with a remote MCP server that uses first-key-wins or strict parsing. In addition, invisible bidirectional/control characters and unsafe characters in MCP argument keys can spoof approval card layout.
4. **Unrecoverable proposal rejections and late `{}` normalization:** Errors from `approval.Gate.Propose` currently abort the entire `HandleMessage` turn without recording a `tool_calls` row (violating Constitution Principle 5), risk echoing untrusted arguments into logs, and leave empty/whitespace arguments unnormalized until after proposal.

## What changes

- **Strict recursive JSON validation (`internal/tools`):** In `Registry.Validate`, normalize empty or whitespace-only arguments to `{}` and run a recursive `json.NewDecoder` token pass before returning early on `nil` schemas. Require a JSON object root, reject duplicate keys at every nesting depth (including inside arrays and across Unicode escape equivalents such as `"id"` and `"\u0069d"`), reject malformed JSON and trailing tokens (`io.EOF`), and wrap failures with `tools.ErrValidation` without echoing untrusted argument keys or values.
- **Critical MCP tool previews (`internal/mcp`):** Update `adapterTool.Preview` to render `"tool <server>__<tool>"` as the header line. Omit argument lines when arguments are empty, whitespace-only, or `{}`. For non-empty objects, defensively validate strict recursive JSON with `UseNumber()`, sort top-level keys, safely quote/escape unsafe keys (containing newlines, `:`, leading/trailing whitespace, or control/bidi characters), and format each entry as `key: <json>` using `json.Encoder` with `SetEscapeHTML(false)` to preserve readable characters and exact JSON numbers.
- **Centralized escaping and shared preview cap (`internal/approval`):** Export `MaxPreviewUTF16 = 3900` and `ErrPreviewTooLong` (with a typed wrapper carrying measured and max UTF-16 units without echoing content). In `Gate.Propose`, defensively normalize empty/whitespace arguments to `{}` before `previewFor`/`ArgsHash`/`ProposeAction`; centrally escape invisible, bidirectional, and control characters (`unicode.Cc` except `\n`, `unicode.Zl`, `unicode.Zp`, `unicode.Cf` except `U+200D` ZWJ) as `\uXXXX` (and `\uXXXX\uYYYY` surrogate pairs for non-BMP runes) across all critical tools; measure UTF-16 code units on the final escaped preview; reject previews exceeding `MaxPreviewUTF16` before `ProposeAction` and `SupersedePending`; and emit content-free structured telemetry (`reason`, `preview_utf16_len`, `preview_utf16_limit`).
- **Recoverable proposal rejections and upfront normalization (`internal/agent`):** Canonicalize empty/whitespace `call.Arguments` to `{}` once at the top of `dispatchToolCalls` before `Validate`, `Propose`, `Execute`, and `RecordToolCall`. When `Approvals.Propose` returns `approval.ErrPreviewTooLong` or `tools.ErrValidation`, record `tool_calls` with `status = rejected`, log a low-cardinality `reason` without untrusted content, and return a `[data]` tool error to the model (naming measured and maximum UTF-16 units for oversized previews) instead of aborting the turn. Keep store/database errors from `Propose` fatal.
- **Notification splitting (`internal/channel/telegram`):** Update `Channel.Notify` to split messages longer than 4096 UTF-16 code units via `split(text)` and send parts in consecutive single-attempt requests without backoff, preventing infinite retry loops on pre-existing oversized `UnnotifiedOutcomes` rows and long MCP admin alerts.

## Capabilities

- `tool-registry` (modified)
- `mcp-integration` (modified)
- `action-approval` (modified)
- `chat-interface` (modified)

## Constitution principles touched

- **1. Approval is enforced in code:** Ensures every critical MCP call shows its exact arguments on the approval card before execution, rejects ambiguous duplicate-key JSON in code, and refuses oversized previews without truncating trailing arguments out of sight.
- **2. Untrusted content:** Treats model-supplied argument keys and values as untrusted data by escaping visual spoofing characters, rejecting malformed/duplicate-key JSON, keeping argument content out of logs and rejection errors, and returning proposal rejections to the model only as `[data]` tool-role messages.
- **5. Everything is observable:** Ensures recoverable proposal rejections still record `rejected` rows in `tool_calls` and emit content-free structured telemetry with rejection reasons and UTF-16 measurements.
- **8. Small, reversible steps:** Fixes the entire preview/validation/delivery pipeline cleanly across existing interfaces with zero database migrations, zero new dependencies, and backward compatibility with existing stored rows.

## Non-goals

- Multi-message approval cards or file attachments for long content.
- Silently or visibly truncating critical approval previews to fit Telegram's message limit (truncation hides trailing arguments from the approving user).
- Database schema migrations.
- Enforcing `additionalProperties: false` on third-party MCP schemas (tracked separately in `nestor-o5b`).
- Introducing separate short vs. full preview columns for follow-up outcome messages.

## Impact

- Packages modified: `internal/tools`, `internal/mcp`, `internal/approval`, `internal/agent`, `internal/channel/telegram`.
- No new external dependencies, environment variables, or database migrations.
- Resolves `nestor-lgd` and the `action-approval` / `mcp-integration` preview drift in `2026-11-23_DRIFT_REPORT.md`; tracked under Beads epic `nestor-dbp`.
