# Reconcile living specifications with shipped behaviour

## Why

Later changes (`add-stale-approval-notices` and `add-tasks-and-reminders`) extended the tool
registry, approval card previews/retries, and outbound chat routing without updating the earlier
requirements in `tool-registry`, `action-approval`, and `chat-interface`. As a result, four
living requirements describe earlier vertical-slice assumptions rather than the behaviour now
tested on `main`:

1. `tool-registry` (`Tool declaration`) still states that enabling demo tools offers only
   `current_time` and `notify_self`, whereas the seven task and reminder tools are always
   registered and demo tools are added alongside them only when `ENABLE_DEMO_TOOLS` is set.
2. `tool-registry` (`Tool results are data`) implies tool-role messages are persisted in
   conversation history, whereas tool calls are recorded in `tool_calls` and sent to the model
   within the turn only as `tool`-role messages marked as untrusted data.
3. `action-approval` (`Approval card`) omits previews resolved from the requesting user's stored
   data (`tools.ContextPreviewer`) and omits re-sending undelivered, unexpired approval cards on
   the owner's next message.
4. `chat-interface` (`Identity resolution at the boundary`) states that only the internal user ID
   is passed in context, omitting the opaque reply chat address (`channel.WithChat`) carried so
   approval cards, reminders, and outcome notices return to the originating chat without exposing
   platform user identities below the channel; `Start a new conversation` also benefits from
   stating explicitly that `/new` closes the open conversation without calling the model so the
   next message opens a fresh conversation.

## What changes

- Update `tool-registry` (`Tool declaration` and `Tool results are data`) to match built-in vs.
  opt-in demo tool registration and in-turn `tool`-role data delivery plus `tool_calls` logging.
- Update `action-approval` (`Approval card`) to include owner-scoped stored-data previews and
  next-message retry of undelivered, unexpired approval cards.
- Update `chat-interface` (`Identity resolution at the boundary` and `Start a new conversation`)
  to distinguish platform sender identity (never passed below the channel) from an opaque reply
  chat routing address (`channel.WithChat`), and to state `/new`'s model-free conversation reset.
- No Go source, SQL migration, or configuration changes are part of this change; every scenario
  maps to tests already passing in the repository.

## Capabilities

- `tool-registry` (modified: two requirements updated)
- `action-approval` (modified: one requirement updated)
- `chat-interface` (modified: two requirements updated)

## Constitution principles touched

- 1 Approval enforced in code: unchanged. Approval card retries and owner-scoped previews never
  execute or approve a critical action.
- 2 Untrusted content: upheld. Tool outputs are sent to the model only as `tool`-role messages
  marked as untrusted data and never concatenated into `system` or `user` messages.
- 5 Observable: upheld. Tool invocations are recorded in `tool_calls` even though `tool`-role
  turns are not stored as conversation history rows.
- 6 User-scoped by design: upheld. Platform sender identities remain confined to the channel
  boundary; `channel.WithChat` carries only an opaque chat routing address for outbound replies,
  cards, reminders, and notices, and `ContextPreviewer` resolves previews strictly under the
  requesting user's ID in context.

## Non-goals

- Refactoring background sweeps (`FailStaleApprovals`, `UsersWithDueReminders`) to iterate per
  active user via `RequireUser` (tracked for a dedicated follow-up code change so Constitution
  principle 6 and `agent-loop` `User isolation` remain strict).
- Filtering disabled allowlist users during background reminder or notice delivery (deferred to
  the follow-up code change).
- Persisting pending-action proposals across turns for conversational edits, injecting current
  time/timezone into the system prompt, or tightening `GetReminder` / `NotifySelfTool` /
  `MarkData` on pending-approval strings (all require runtime code changes and belong in separate
  OpenSpec changes).
- Top-level documentation and deployment config cleanups (`README.md`, `deploy/litellm.yaml`).

## Impact

- Spec-only change across `openspec/specs/tool-registry/spec.md`,
  `openspec/specs/action-approval/spec.md`, and `openspec/specs/chat-interface/spec.md` when
  archived.
- Zero runtime, schema, or dependency changes.
