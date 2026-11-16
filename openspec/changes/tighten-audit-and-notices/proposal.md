# Proposal: tighten audit trail, outcome notices, and schema cleanup

## Why

The drift audit identified four low-priority gaps across observability, approval notices, and
early schema/helper definitions:

1. **Unlinked audit foreign keys and unrecorded critical execution latency:** `tool_calls.model_call_id`
   and `pending_actions.tool_call_id` exist in PostgreSQL (`0002_tool_calls.up.sql`,
   `0003_pending_actions.up.sql`) but are always `NULL`, and when a `critical` tool executes after
   the owner taps **Approve**, the linked `tool_calls` row stays in status `proposed` without
   execution latency, result, or error.
2. **Incomplete structured logs:** `Agent.dispatchToolCalls` emits no structured `slog` entry for
   tool dispatches, `approval.SendOutcomeNotice` logs failures but not successful deliveries, and
   background ticks (`approval.SweepStale`, `reminders.deliverDue`) log with `user_id` but no
   correlation `request_id`.
3. **Zero-chat stale approval notice gap:** If sending an approval card failed before a chat ID
   was recorded (`notify_chat_id = 0`) and the action later fails as outcome unknown,
   `Agent.resendOutcomeNotices` keeps passing `NotifyChatID = 0` on every subsequent user message
   instead of falling back to the incoming message's chat, leaving the notice permanently
   undelivered and logging a warning on every turn.
4. **Unused schema column, status value, and helpers:** `user_identities.chat_id`
   (`0001_chat_foundation.up.sql`), `store.ToolCallStatusPending` (`'pending'` in
   `0002_tool_calls.up.sql`), and standalone `tools.Validate(t Tool, args)` (`internal/tools`) are
   unused anywhere in runtime code, while `cmd/nestor` duplicates `tools.RegisterDemoTools` inline.

## What changes

- Return the inserted row ID from `Store.RecordModelCall` and `Store.RecordToolCall`, link
  `tool_calls.model_call_id` during agent tool dispatch, link `pending_actions.tool_call_id` via
  `Store.LinkActionToolCall`, and update the linked `tool_calls` row with post-approval execution
  status, result/error, and latency via `Store.CompleteToolCall` in `approval.Gate`.
- Add migration `0007_audit_and_schema_cleanup` enforcing composite `(id, user_id)` foreign keys on
  `tool_calls.model_call_id` and `pending_actions.tool_call_id`, dropping unused
  `user_identities.chat_id`, and removing unused `'pending'` from `tool_calls.status`.
- Emit structured `slog` events (without message text or tool arguments/results) on tool call
  completion and outcome-notice delivery, and attach a fresh `obs.WithRequestID` to each
  background approval sweep and reminder delivery tick.
- Fall back to `channel.ChatFrom(ctx)` in `Agent.resendOutcomeNotices` when an unnotified action
  has `NotifyChatID == 0`.
- Wire `tools.RegisterDemoTools` in `cmd/nestor/main.go`, and remove unused standalone
  `tools.Validate` and `store.ToolCallStatusPending`.

## Capabilities

- `observability` (modified): structured log events cover tool dispatches, outcome notices, and
  `request_id` correlation on background sweeps and reminder ticks.
- `tool-registry` (modified): `tool_calls` rows record `model_call_id` and post-approval critical
  execution outcomes and latency.
- `action-approval` (modified): `pending_actions` records `tool_call_id`, and unnotified stale
  outcomes without a recorded card chat fall back to the chat of the owner's next message.

## Non-goals

- Persisting intermediate `assistant` tool-call messages or `tool`-role results to `messages`.
- Adding model tier routing, cost budgets, or new user-facing tools (`add-model-routing`).
- Modifying `add-mcp-servers` or `add-skills` proposals.

## Constitution principles touched

- **1. Approval is enforced in code:** Critical tool execution remains exclusively inside
  `internal/approval`; `CompleteToolCall` records audit state after execution without adding any
  new execution path.
- **2. Untrusted content:** Structured logs record only tool names, risk levels, statuses, action
  IDs, and latencies—never tool arguments, previews, or outputs.
- **5. Everything is observable:** Every model call, tool call (including post-approval execution
  latency and outcome), pending action, and background sweep is linked in SQL and traceable via
  structured JSON logs with `request_id` and `user_id`.
- **6. User-scoped by design:** `LinkActionToolCall` and `CompleteToolCall` take `identity.UserID`
  explicitly and call `store.RequireUser`, backed by composite `(id, user_id)` foreign keys in
  PostgreSQL so cross-user linking is impossible.
