## Why

Several runtime behaviours around conversational context and critical-action proposals either fall short of the living specs or leave gaps between turns. When a user replies to edit a proposed action ("make it 4pm"), the follow-up model request omits the open proposal because tool-call messages are not stored in `messages`. Conversely, when a user asks for two deletions or cancellations in a single turn, `Gate.Propose` unconditionally supersedes the first proposal before the user can approve it. In addition, the model receives no current local clock or `TIMEZONE` when `ENABLE_DEMO_TOOLS` is disabled, `GetReminder` returns already-delivered or cancelled reminders during `reminder_cancel` previews, `notify_self` accepts blank `text` past schema validation, `"pending approval <id>"` tool results omit `tools.MarkData`, and the structural critical-execution check skips `cmd/`.

## What changes

- Add optional `Location *time.Location` and `Now func() time.Time` to `agent.Options` (wired in `cmd/nestor` from `TIMEZONE`) so each turn's system prompt includes the current local weekday, date, time, and IANA time zone when configured.
- Add user-scoped `ProposedActions` to `store.Store` (`postgres.Store` and `testutil.FakeStore`) and reconstruct unexpired `proposed` actions from the active conversation in `Agent.prompt` as `assistant` `ToolCalls` + marked `tool`-role `"pending approval <id>"` messages without placing arguments or previews into `system` messages.
- Scope superseding to prior turns via `approval.WithTurn` and `SupersedePending(..., keepIDs ...string)` so multiple critical calls of the same tool in one turn stay `proposed` with separate approval cards, while a proposal in a later turn supersedes earlier turns' open proposals for that tool.
- Mark `"pending approval <id>"` tool results with `tools.MarkData`, filter `GetReminder` to pending reminders (plus the 500-rune limit in `FakeStore.AddReminder`), reject empty/whitespace `notify_self` `text` in JSON Schema validation, and include `cmd/` in `TestStructuralCriticalToolExecution`.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `agent-loop`: Include the current local weekday, date, time, and IANA time zone in the system prompt when a location is configured on the agent.
- `action-approval`: Keep unexpired proposed actions in model context across turns via `assistant`/`tool` messages and supersede open proposals of the same tool only across turns, leaving multiple proposals from the same turn active.
- `reminders`: Treat delivered or cancelled reminders as not found in `GetReminder` so `reminder_cancel` previews and executions only resolve pending reminders.
- `tool-registry`: Mark pending-approval tool results as untrusted data and reject blank `notify_self` text during schema validation before proposal.

## Constitution principles touched

- **1. Approval is enforced in code:** Every critical tool call still creates a `pending_actions` row in state `proposed` and executes only through `approval.Gate.Decide` on an owner callback. Keeping same-turn proposals active requires separate explicit approval for each card, and `TestStructuralCriticalToolExecution` now also verifies `cmd/`.
- **2. Content from tools is untrusted data:** Both executed/error tool outputs and `"pending approval <id>"` results are wrapped with `tools.MarkData` in `tool`-role messages; reconstructed proposals use `assistant` tool calls paired with marked `tool` messages and never place arguments or previews into `system` or `user` messages.
- **4. Personal data stays on the host:** Only the requesting user's active conversation history, unexpired proposals from that conversation, and local clock/zone metadata are sent to the proxy.
- **6. User-scoped by design:** `ProposedActions`, `SupersedePending`, and `GetReminder` require matching `identity.UserID` in `ctx` via `store.RequireUser` and filter SQL queries by `user_id`.

## Non-goals

- Persisting raw `tool`-role outputs into the `messages` table across turns (which would bloat conversation history with large tool payloads).
- Changing reminder clock-only parsing (`HH:MM` rollover) or task `YYYY-MM-DD` storage semantics.
- Adding inline "Edit" buttons or modal forms on Telegram approval cards.

## Impact

- `internal/store/store.go`, `internal/store/postgres/store.go`, `internal/store/postgres/tasks.go`, `internal/store/postgres/scoping_test.go`, `internal/store/postgres/approval_store_test.go`, `internal/store/postgres/reminders_store_test.go`
- `internal/testutil/fakes.go`, `internal/testutil/tasks_fake.go`
- `internal/approval/approval.go`, `internal/approval/approval_test.go`
- `internal/agent/agent.go`, `internal/agent/agent_test.go`
- `internal/reminders/reminders_test.go`
- `internal/tools/demo.go`, `internal/tools/demo_test.go`
- `cmd/nestor/main.go`, `cmd/nestor/main_test.go`, `evals/tasks/tasks_eval_test.go`
