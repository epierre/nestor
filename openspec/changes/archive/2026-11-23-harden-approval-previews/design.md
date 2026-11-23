# Design: harden approval previews

## Architecture and data flow

Every tool call flows through one pipeline from the agent loop to Telegram delivery:

```
model ToolCall
  │
  ▼  1. agent.dispatchToolCalls
  │     Canonicalize empty/whitespace call.Arguments -> "{}"
  │     Lookup + Enabled check
  ▼  2. tools.Registry.Validate
  │     Strict recursive JSON check (object root, no dup keys, io.EOF) BEFORE sch == nil
  │     + JSON Schema validation (when sch != nil)
  ▼  3. approval.Gate.Propose (for RiskCritical)
  │     Defensive "{}" normalization -> previewFor (adapterTool.Preview / ContextPreviewer / tool.Preview)
  │     -> centralized bidi/control escaping -> UTF-16 measurement <= MaxPreviewUTF16 (3900)
  │     -> store.ProposeAction -> store.SupersedePending
  ▼  4. telegram.Channel.SendApprovalCard / outcomeMessage / OutcomeNoticeText
        Action.Preview + template wrapper <= 4096 UTF-16 units
        Channel.Notify splits >4096 UTF-16 messages into consecutive single-attempt sends
```

## Strict recursive JSON validation

`tools.Registry.Validate` previously returned `nil` immediately when `sch == nil` (which occurs for tools registered without a schema, or could mask raw JSON edge cases) and delegated parsing solely to `jsonschema.UnmarshalJSON`, which uses standard Go decoding semantics where duplicate keys resolve by last-key-wins.

To eliminate parser differentials between Nestor's preview/validation layer and remote MCP servers:

1. If `len(bytes.TrimSpace(args)) == 0`, normalize `args` to `json.RawMessage("{}")`.
2. Before checking `if sch == nil`, run a recursive `json.NewDecoder` token pass (`UseNumber()`):
   - Expect an initial `json.Delim('{')` token (object root required; arrays, scalars, and `null` are rejected).
   - Within every object at any depth (including objects nested inside other objects or arrays), read each key via `dec.Token()` as a Go `string` (which decodes JSON escapes so `"id"` and `"\u0069d"` produce the identical string `"id"`), check a per-object `seen map[string]struct{}` for duplicates, and recursively validate the value.
   - Within every array, recursively validate each element until `json.Delim(']')`.
   - After the root object closes, call `dec.Token()` again and require `io.EOF` so trailing tokens (e.g., `{}{}`) are rejected.
3. Wrap any strict JSON failure with `tools.ErrValidation` using static, content-free error messages (never echoing untrusted argument keys or values).
4. If `sch != nil`, continue to run `jsonschema.UnmarshalJSON` and `sch.Validate`.

## MCP preview rendering

`internal/mcp.adapterTool.Preview(args json.RawMessage)` replaces the static `"MCP tool <server>__<tool>"` string with deterministic argument rendering:

- Header line: `"tool " + t.name` (i.e., `tool <server>__<tool>`).
- If `args` is empty, whitespace-only, or decodes to an empty object `{}`: return only the header line (`"tool " + t.name`).
- Otherwise, defensively run strict recursive JSON validation with `json.NewDecoder` + `UseNumber()` (rejecting duplicate keys, non-object roots, malformed JSON, and trailing tokens wrapped in `tools.ErrValidation` without echoing values).
- Sort top-level keys lexicographically (`slices.Sort`).
- For each top-level key, append a line `renderedKey + ": " + renderedValue`:
  - **Key rendering:** If the key is empty, contains `\n`, `\r`, `:`, leading or trailing whitespace, or any control/bidi/separator rune (`unicode.Cc`, `unicode.Zl`, `unicode.Zp`, `unicode.Cf`), JSON-quote it safely with `json.Marshal` (escaping `<`, `>`, `&` is disabled via a `SetEscapeHTML(false)` encoder so readable keys stay clear while control/quote/newline characters are escaped). Otherwise emit the raw key.
  - **Value rendering:** Encode each value on one line using a `json.NewEncoder` configured with `SetEscapeHTML(false)` (stripping the trailing `\n` added by `Encoder.Encode`). Because values were decoded with `UseNumber()`, 64-bit integers such as `9007199254740993` are preserved verbatim instead of losing precision in `float64`, `<`/`>`/`&` remain readable, and nested objects/arrays stay compact with embedded newlines escaped as `\n`.

## Centralized escaping and UTF-16 budget

Centralizing sanitization and length enforcement in `approval.Gate.Propose` protects both MCP previews and every built-in or future critical tool (`notify_self`, `task_delete`, `reminder_cancel`, and future `ContextPreviewer` tools) in one place:

1. **Defensive empty-arg normalization:** If `len(bytes.TrimSpace(args)) == 0`, set `args = json.RawMessage("{}")` before calling `previewFor`, `ArgsHash`, and `ProposeAction` so hashes and PostgreSQL `json NOT NULL` rows always agree.
2. **Centralized invisible/bidi/control escaping:** Escape every rune `r` in the preview string that belongs to:
   - `unicode.Cc` except `'\n'` (intentional multi-line structure is preserved, while `\r`, `\t`, NUL, and other C0/C1 controls are escaped),
   - `unicode.Zl` (line separator `U+2028`),
   - `unicode.Zp` (paragraph separator `U+2029`),
   - `unicode.Cf` except `'\u200D'` (Zero Width Joiner is preserved so joined emoji like family/flag sequences stay intact, while `U+202E` RLO, `U+200B` ZWSP, and tag characters `U+E0000..U+E007F` are escaped).
   BMP runes are formatted as `\uXXXX` (4 lowercase hex digits); non-BMP runes are encoded via `utf16.EncodeRune(r)` as JSON-style surrogate pairs `\uXXXX\uYYYY`.
3. **UTF-16 measurement and cap:** Measure the final escaped preview by summing `utf16.RuneLen(r)` across `range escaped`. Export `MaxPreviewUTF16 = 3900` and `ErrPreviewTooLong` (wrapped by a typed error carrying `Measured int` and `Max int` without any preview text). If `utf16Len > MaxPreviewUTF16`, return the error before calling `g.store.ProposeAction` or `g.store.SupersedePending`, and emit a content-free `slog.WarnContext` log with `"reason"`, `"preview_utf16_len"`, and `"preview_utf16_limit"`.

### UTF-16 budget math

Telegram Bot API enforces `maxMessageLength = 4096` UTF-16 code units per message (`internal/channel/telegram/telegram.go`). Every template that embeds `action.Preview` fits within `4096` units when `Preview <= 3900` units:

| Template | Wrapper text | Wrapper UTF-16 units | Max total (`3900 + wrapper`) | Headroom |
|---|---|---|---|---|
| `approval.OutcomeNoticeText` | `"Outcome unknown, not run again: "` (32) + `"\nIt may or may not have run. Nothing will be retried."` (53) | 85 | 3985 | 111 |
| `outcomeMessage` (`ErrIntegrity`) | `"Not run, its details changed after the proposal: "` | 49 | 3949 | 147 |
| `SendApprovalCard` (`cardQuestion`) | `"\n\nApprove this action?"` | 22 | 3922 | 174 |
| `outcomeMessage` (`PendingExpired`) | `"Expired, not run: "` | 18 | 3918 | 178 |
| `outcomeMessage` (`PendingRejected`) | `"Rejected: "` | 10 | 3910 | 186 |
| `outcomeMessage` (`PendingFailed`) | `"Failed: "` | 8 | 3908 | 188 |
| `outcomeMessage` (`PendingExecuted`) | `"Done: "` | 6 | 3906 | 190 |

A 111-unit headroom remains even on the longest notice (`OutcomeNoticeText`), leaving room for minor wording adjustments while guaranteeing single-message card delivery without truncation.

## Error taxonomy and telemetry

- **`approval.ErrPreviewTooLong`:** Sentinel error exported from `internal/approval`, wrapped by a typed error exposing measured and maximum UTF-16 unit counts (`Measured`, `Max`) and formatting only those counts in `Error()` (never preview or argument content).
- **`tools.ErrValidation`:** Sentinel error returned for strict JSON validation failures in `Registry.Validate` and `adapterTool.Preview` without echoing untrusted arguments.
- **`agent.dispatchToolCalls` handling:**
  - Canonicalize empty/whitespace `call.Arguments` to `{}` once before `Validate`, `Propose`, `Execute`, and `RecordToolCall`.
  - Classify `Propose` errors using `errors.Is`:
    - `approval.ErrPreviewTooLong`: record `tool_calls` with `status = rejected`, emit low-cardinality structured log (`reason: "preview_too_long"`), and return `[data]` tool error `"error: approval preview too long (<measured> UTF-16 units; max <max>)"` so the model can shorten the call or inform the user in the same turn.
    - `tools.ErrValidation`: record `tool_calls` with `status = rejected`, emit low-cardinality structured log (`reason: "invalid_preview_args"`), and return `[data]` tool error `"error: invalid arguments: ..."` without untrusted content.
    - All other errors from `Propose` (database/store errors): remain fatal and abort `HandleMessage` so failed persistence is never silently turned into a model tool error.
  - Other recoverable rejections in `dispatchToolCalls` use low-cardinality structured reason tags (`unknown_tool`, `invalid_arguments`, `no_approval_gate`) without logging argument values.

## ContextPreviewer bounded-excerpt rule

Because `Gate.Propose` enforces a hard `3900` UTF-16 unit cap without truncation, future critical tools implementing `tools.ContextPreviewer` (such as outgoing email or calendar invite tools in `add-email` and `add-calendar`) MUST construct compact previews from metadata, draft identifiers, or bounded excerpts rather than embedding full multi-kilobyte message bodies or attendee lists directly into the preview string.

## Notification splitting

`telegram.Channel.Notify` currently sends `text` in a single unsplit `sendMessage` request. Pre-existing database rows in `UnnotifiedOutcomes` (created before this change) with previews exceeding `4011` UTF-16 units—or long MCP admin alerts exceeding `4096` units—fail permanently on every user message in `resendOutcomeNotices`.

Updating `Channel.Notify` to iterate `for _, part := range split(text)` and issue consecutive single-attempt `sendMessage` calls preserves `Notify`'s non-blocking single-attempt contract (no retry/backoff loop) while allowing oversized notices and admin alerts to deliver cleanly in order.

## Failure modes

- **Model/provider down:** Handled by existing `Agent.HandleMessage` notice (`noticeModelFailure`); unchanged.
- **Database down during `Propose`:** `Gate.Propose` returns a store error (not `ErrPreviewTooLong` or `ErrValidation`), which `dispatchToolCalls` propagates out of `HandleMessage` as a fatal turn error so no unpersisted approval card or missing audit row occurs.
- **Telegram down during `SendApprovalCard` or `Notify`:** `SendApprovalCard` returns the transport error and leaves the card unsent for `resendCards` on the user's next message. `Notify` returns the error immediately without backoff so callers decide whether to retry later (`resendOutcomeNotices` keeps the notice pending).
- **Oversized or malformed critical proposal:** Refused cleanly before storing a pending action or superseding earlier open proposals; `tool_calls` logs `rejected`, structured logs record content-free reason and UTF-16 lengths, and the model continues the turn with an actionable `[data]` tool result.

## Dependencies

Zero new external dependencies. All JSON token validation, UTF-16 measurement, and Unicode category checks use the Go standard library (`encoding/json`, `unicode`, `unicode/utf16`, `slices`, `io`, `bytes`).

## Testing approach

- `internal/tools`: `TestValidateStrictJSON` covering top-level/nested/array/Unicode-escaped duplicate keys, non-object roots, trailing tokens, empty/whitespace args, and `nil`-schema rejection.
- `internal/approval`: `TestProposeEscapesBidiAndControlChars`, `TestProposePreviewLengthCap` (exact 3900 ASCII and 2-unit surrogate-pair emoji pass, 3901 fails without storing or superseding, escape expansion counted before cap, empty args normalized), and `TestProposeRejectionStructuredLogs`.
- `internal/mcp`: `TestAdapterToolPreview` and updated `TestCriticalToolCreatesPendingAction` covering empty/`{}`/whitespace args, sorted keys, exact 64-bit integer `9007199254740993`, unescaped `<>&`, compact nested values, quoted unsafe keys, and strict error handling without leaking values.
- `internal/agent`: `TestDispatchToolCallsProposalRejections` covering oversized `notify_self` and MCP calls, duplicate-key rejection, upfront `{}` normalization, preserved prior pending actions, `rejected` `tool_calls` rows, and fatal DB errors.
- `internal/channel/telegram`: `TestNotifySplitsOversizedMessages` and cross-package invariant test `TestApprovalMaxPreviewFitsAllTemplates` proving exact `approval.MaxPreviewUTF16` (3900) ASCII and surrogate-pair emoji previews fit `<= 4096` UTF-16 units across `SendApprovalCard`, `approval.OutcomeNoticeText`, and all six `outcomeMessage` variants.
