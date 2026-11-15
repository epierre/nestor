## Context

Four small runtime gaps remain after reconciling the living specs and scoping background sweeps:

1. **Clock and time zone context (`agent-loop`):** `ENABLE_DEMO_TOOLS` is `false` by default, so `current_time` is not registered in production. While `reminders.Tools` parses times in `TIMEZONE`, `cmd/nestor` passes a static `systemPrompt` with no date, weekday, clock time, or time zone, leaving the model unable to resolve relative phrases ("tomorrow", "in 2 hours", "next Monday") into concrete `YYYY-MM-DD` or `YYYY-MM-DD HH:MM` arguments.
2. **Conversational edits vs. multi-item proposals (`action-approval`):**
   - `Agent.HandleMessage` stores only `user`, `assistant` (final prose), and `system` (outcome note) rows in `messages`. When a user replies "make it 4pm" to an open proposal, the next turn's model request lacks the earlier critical tool call and its `"pending approval <id>"` result.
   - Conversely, `Gate.Propose` calls `SupersedePending(ctx, user, tool.Name(), id)` unconditionally, so if the model proposes two `task_delete` or `reminder_cancel` calls in a single turn ("delete tasks 3 and 4"), the second call immediately marks the first `superseded` before the user can tap Approve on both cards.
3. **Pending-only `GetReminder` (`reminders`):** `store.ErrReminderNotFound` documents that delivered and cancelled reminders are treated as not found, and `ListReminders`, `DueReminders`, `CancelReminder`, and `MarkReminderDelivered` all filter `delivered_at IS NULL AND cancelled_at IS NULL`. `GetReminder` in `postgres.Store` and `testutil.FakeStore` omitted that predicate, causing `reminder_cancel.PreviewFor` to build a normal `"Cancel reminder: <text>"` card for an already-delivered or cancelled reminder. `FakeStore.AddReminder` also omitted the 500-rune ceiling enforced by PostgreSQL.
4. **Data-marking, schema validation, and structural AST guard (`tool-registry`, `action-approval`):**
   - `Agent.dispatchToolCalls` sends `"pending approval " + action.ID` without `tools.MarkData(...)`.
   - `NotifySelfTool.Schema()` requires `"text"` of type `"string"` without non-blank constraints, letting `{"text": ""}` or `{"text": "   "}` create a pending action before failing at execution.
   - `TestStructuralCriticalToolExecution` walks `internal/` only, omitting `cmd/`.

## External dependencies

None — standard library and existing module dependencies only.

## Decisions

### 1. Optional `Location` and `Now` on `agent.Options`

Add two optional fields to `agent.Options`:
- `Location *time.Location`: when non-nil, `Agent.prompt` appends a line to the system prompt formatted from the current clock in `Location`:
  `Current time: Monday, 2006-01-02 15:04 MST (<Location.String()>).`
- `Now func() time.Time`: clock used by `Agent` (defaulting to `time.Now` when nil) for both the prompt clock and querying unexpired `ProposedActions`.

Leaving `Location` nil preserves `SystemPrompt` verbatim so unit tests with synthetic prompts remain deterministic and unchanged. `cmd/nestor` passes `loc` (already loaded from `cfg.Timezone` before constructing `registry`) into `agentOptions(cfg, registry, loc)`.

### 2. Reconstructing open proposals in `Agent.prompt` without polluting `system` messages

Constitution principle 2 and `internal/approval` forbid placing untrusted tool inputs or previews into `system` or `user` messages (since an attacker-controlled tool output in turn 1 could have influenced the proposed arguments).

- Add `ProposedActions(ctx context.Context, user identity.UserID, now time.Time) ([]PendingAction, error)` to `store.Store`:
  - Calls `store.RequireUser(ctx, user)`.
  - Returns rows for `user` with `state = 'proposed' AND expires_at > now` ordered by `created_at, id`.
- In `Agent.HandleMessage`, load `proposed, err := a.store.ProposedActions(ctx, user, a.now())` alongside `RecentMessages`.
- Filter `proposed` to actions created during the active conversation (`conversation.StartedAt.IsZero() || !p.CreatedAt.Before(conversation.StartedAt.Truncate(time.Microsecond))`) so `/new` still starts a clean conversation context.
- When `history` contains at least one prior `user` message before the newly appended `user` message (`len(history) > 1`), splice the active proposed actions right after the prior `user` message to which each belongs (the last `RoleUser` message in `history[:len(history)-1]` whose timestamp is not after `p.CreatedAt`, falling back to the first `RoleUser` in `history[:len(history)-1]`):
  - One `model.Message{Role: model.RoleAssistant, ToolCalls: ...}` listing `{ID: "pending_" + p.ID, Name: p.ToolName, Arguments: p.ArgsJSON}`.
  - Matching `model.Message{Role: model.RoleTool, ToolCallID: "pending_" + p.ID, Content: tools.MarkData("pending approval " + p.ID)}` entries.
- This produces a valid `user -> assistant(tool_calls) -> tool -> ... -> user` sequence for OpenAI, Gemini, and Anthropic via LiteLLM, gives the model the exact tool name, arguments, and pending action ID needed for conversational edits, and never places arguments or previews into `system` or `user` messages. Once an action transitions out of `proposed` (superseded, approved, rejected, expired, or failed), it disappears from `ProposedActions`.

### 3. Turn-scoped superseding via `approval.WithTurn`

- Add `approval.WithTurn(ctx context.Context) context.Context` holding a turn-local slice of action IDs proposed during the current `Agent.HandleMessage` call (guarded by a mutex).
- Extend `Store.SupersedePending(ctx context.Context, user identity.UserID, tool, byID string, keepIDs ...string) (int64, error)` to exclude both `byID` and any `keepIDs` (`NOT (id = ANY($4))` in PostgreSQL).
- In `Gate.Propose`, after inserting `action.ID`:
  - Record `action.ID` in the turn tracker if `ctx` carries one, obtaining all IDs proposed so far in this turn (`keepIDs`).
  - Call `g.store.SupersedePending(ctx, user, tool.Name(), id, keepIDs...)`.
- Outcome:
  - Multiple `task_delete` or `reminder_cancel` calls in a single `HandleMessage` turn share `keepIDs`, so none supersedes a sibling from the same turn; every card remains `proposed` and requires its own explicit user approval.
  - A critical call for the same tool in a subsequent `HandleMessage` turn starts with a fresh `WithTurn(ctx)`, so `keepIDs` contains only the new turn's ID(s) and supersedes all `proposed` actions of that tool from earlier turns.
  - Direct calls to `Gate.Propose` outside `WithTurn` pass no extra `keepIDs` and continue superseding earlier `proposed` actions of the same tool immediately.

### 4. Tightening `GetReminder`, `MarkData`, `NotifySelfTool.Schema`, and AST check

- `postgres.Store.GetReminder` and `testutil.FakeStore.GetReminder` require `delivered_at IS NULL AND cancelled_at IS NULL`, returning `store.ErrReminderNotFound` otherwise; `FakeStore.AddReminder` rejects `len([]rune(r.Text)) > 500`.
- `Agent.dispatchToolCalls` wraps `"pending approval " + action.ID` with `tools.MarkData(...)` while keeping `ResultJSON` as the marshalled `"pending approval <id>"` string (consistent with executed tools, which marshal `res.Content` into `ResultJSON` and pass `tools.MarkData(res.Content)` to `RoleTool`).
- `NotifySelfTool.Schema()` adds `"minLength": 1, "pattern": "\\S"` on `text` so empty or whitespace-only strings fail `Registry.Validate` before `Gate.Propose`.
- `TestStructuralCriticalToolExecution` walks both `internal/` and `cmd/`.

## Failure modes

- **Model provider down:** Unchanged. `Agent.HandleMessage` records failed attempts and returns the short failure notice; open `proposed` actions remain untouched in PostgreSQL and reappear in context on the next turn.
- **Database down:** If `ProposedActions` fails when building the prompt, `HandleMessage` returns a wrapped error and makes no model call, matching `ActiveConversation` and `RecentMessages`. If `SupersedePending` fails after `ProposeAction`, `Gate.Propose` logs the error and leaves both actions `proposed`, each still requiring explicit owner approval (fail-closed).
- **Telegram down:** Unchanged. Undelivered approval cards remain `card_sent_at IS NULL` and are retried on the owner's next message for every action that is still `proposed` (now including all sibling cards from a multi-item turn).
