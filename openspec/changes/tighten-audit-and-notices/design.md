# Design: tighten audit trail, outcome notices, and schema cleanup

## Dependencies

None — uses the Go standard library and existing `github.com/jackc/pgx/v5`.

## Store and schema changes

### Store interface (`internal/store`)

- `RecordModelCall(ctx context.Context, user identity.UserID, call ModelCall) (int64, error)` and
  `RecordToolCall(ctx context.Context, user identity.UserID, call ToolCall) (int64, error)` return
  the inserted row ID (`RETURNING id` in PostgreSQL; sequential ID in `testutil.FakeStore`).
  Existing test call sites of `RecordModelCall` and `RecordToolCall` are updated to discard the
  returned ID (`_, err := ...`) without altering any scenario assertion.
- `PendingAction` gains `ToolCallID *int64`, read by `scanPending` and written by `ProposeAction`
  when non-nil.
- `LinkActionToolCall(ctx context.Context, user identity.UserID, actionID string, toolCallID int64) error`
  sets `pending_actions.tool_call_id` for `user`'s action after verifying `RequireUser(ctx, user)`
  and confirming the target `tool_calls` row belongs to `user` (returning `ErrActionNotFound` if
  either row is missing or belongs to another user).
- `CompleteToolCall(ctx context.Context, user identity.UserID, id int64, status string, resultJSON json.RawMessage, errText string, latency time.Duration) error`
  updates `status`, `result_json`, `error`, and `latency_ms` (rounding sub-millisecond `latency > 0`
  up to `1`, matching `RecordToolCall`) on `user`'s `tool_calls` row.
- Unused constant `store.ToolCallStatusPending` (`"pending"`) is removed.

### Migration `0007_audit_and_schema_cleanup`

`0007_audit_and_schema_cleanup.up.sql`:
1. Drops unused column `user_identities.chat_id`.
2. Replaces `tool_calls_status_check` with `CHECK (status IN ('executed', 'rejected', 'error', 'proposed'))`
   (converting any legacy `'pending'` rows to `'proposed'` first if present).
3. Adds `UNIQUE (id, user_id)` on `model_calls` and `tool_calls`, adds composite foreign key
   `(model_call_id, user_id) REFERENCES model_calls (id, user_id)` on `tool_calls`, and replaces
   `pending_actions_tool_call_id_fkey` with composite foreign key
   `(tool_call_id, user_id) REFERENCES tool_calls (id, user_id)`. With PostgreSQL's default
   `MATCH SIMPLE`, nullable `model_call_id` / `tool_call_id` remains allowed when `NULL` (such as
   direct unit tests of `Gate.Propose`), while non-null links are guaranteed by the schema to
   belong to the same `user_id`.

`0007_audit_and_schema_cleanup.down.sql` reverses each step cleanly so `MigrateDown` passes.

## Agent, approval, and scheduler flows

1. **Model-to-tool-to-action linkage (`internal/agent` & `internal/approval`):**
   - `Agent.recordCalls` returns the ID of the last recorded `ModelCall` (`*int64`) and passes it
     into `Agent.dispatchToolCalls`.
   - Every `store.ToolCall` written in `dispatchToolCalls` sets `ModelCallID`.
   - When a `critical` tool call is proposed (`status = 'proposed'`), `dispatchToolCalls` calls
     `a.store.LinkActionToolCall(ctx, user, action.ID, toolCallID)` right after `RecordToolCall`.
   - When an approved action runs in `approval.Gate.run`, execution duration is measured around
     `tool.Execute`. If `action.ToolCallID != nil`, `CompleteToolCall` updates the linked
     `tool_calls` row to `executed` (with `resultJSON` and `latency`) or `error` (with the failure
     text and `latency`). Because the critical tool outcome is already final, any `CompleteToolCall`
     failure is logged with `slog.ErrorContext` rather than turning an executed action into an error.
2. **Structured logging (`internal/agent`, `internal/approval`, `internal/reminders`):**
   - `dispatchToolCalls` logs `"tool call finished"` with `tool`, `risk`, `status`, and
     `latency_ms` (no arguments or outputs).
   - `approval.Gate.run` / `fail` logs `"critical tool executed"` / `"critical tool failed"` with
     `action_id`, `tool`, and `latency_ms` (or `error`).
   - `approval.SendOutcomeNotice` logs `"outcome notice sent"` with `action_id` once
     `MarkOutcomeNoticed` succeeds.
   - `approval.SweepStale` and `reminders.deliverDue` attach `obs.WithRequestID(ctx, obs.NewRequestID())`
     at the start of each sweep/tick so all log lines emitted during one run share a `request_id`
     alongside per-user `user_id`.
3. **Zero-chat outcome notice fallback (`internal/agent`):**
   - In `Agent.resendOutcomeNotices`, if `action.NotifyChatID == 0` and `channel.ChatFrom(ctx)`
     carries a non-zero chat ID from the owner's incoming message, `action.NotifyChatID` is set to
     that chat ID before calling `approval.SendOutcomeNotice`.
4. **Helper cleanup (`internal/tools` & `cmd/nestor`):**
   - `cmd/nestor/main.go` (`toolRegistry`) calls `tools.RegisterDemoTools(reg, enableDemo, notifySelf(notifier))`
     instead of duplicating registration inline.
   - Unused standalone `tools.Validate(t Tool, args)` in `internal/tools/registry.go` is removed
     (`Registry.Validate` compiles schemas once at registration and remains the single validator).

## Failure modes

- **Model provider down:** `recordCalls` records the failed attempt (`status = 'error'`), no tool
  calls are dispatched, and the user receives the existing model-failure notice.
- **Database down:**
  - During `RecordModelCall`, `RecordToolCall`, or `LinkActionToolCall`: `HandleMessage` returns an
    error so nothing reaches the user unrecorded.
  - During `CompleteToolCall` after a critical tool has already executed: `pending_actions` holds
    the authoritative execution state (or becomes stale if `UpdatePendingAction` also failed) and
    `CompleteToolCall` logs an error without re-running the tool.
- **Telegram down:**
  - When sending an approval card fails at proposal time (`notify_chat_id` stays `0`) and the
    action is later failed as outcome unknown before its card was ever delivered, the owner's next
    Telegram message supplies `channel.ChatFrom(ctx)` so `resendOutcomeNotices` delivers the notice
    once and marks `outcome_notice_sent_at`.
