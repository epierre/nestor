# Add tool registry and approval gate

## Why

Every useful capability on the roadmap (tasks, calendar, email) is a tool the model can call,
and some of those tools have real-world consequences. The safety guarantee in the vision
("zero critical actions without a recorded approval") must exist *before* the first dangerous
tool does, and it must be enforced by code paths the model cannot influence. Building the
gate first, against harmless demo tools, lets it be tested in isolation.

## What changes

- A tool registry: each tool declares a name, description, JSON Schema for arguments and a
  risk level (`read`, `reversible`, `critical`).
- The agent loop executes tool calls through the registry: argument validation, execution,
  result returned to the model as a tool message.
- Tool calls at risk level `critical` are not executed. A pending action owned by the requesting
  user is stored and that user receives an approval card (Telegram inline buttons: Approve /
  Reject; replying with changes counts as Edit).
- Approval binds to the exact arguments shown and to the action's user. Execution happens only
  from that user's button press, exactly once, and the outcome is reported back into their
  conversation.
- Tools execute with the requesting user's ID in context, so per-user tools (calendar, email)
  can only ever act on that user's account.
- Pending actions expire.
- Two built-in demo tools to exercise the paths: `current_time` (read) and `notify_self`
  (critical; sends a message to the requesting user's own chat). `notify_self` is enabled by a config flag.
- An eval scenario set under `evals/approval/` with adversarial prompts.

## Capabilities

- `tool-registry` (new)
- `action-approval` (new)

## Constitution principles touched

- 1 Approval enforced in code: execution of a critical tool has exactly one code path, triggered
  by a callback from the action's own user, checked against a stored approval.
- 2 Untrusted content: tool results enter the conversation as tool messages only and can never
  produce an approval.
- 5 Observable: every tool call, proposal, decision and execution is recorded.
- 6 User-scoped: tool calls and pending actions carry a user ID; one user can never see,
  approve or reject another user's action.

## Non-goals

- Real tasks, calendar or email tools (later changes).
- MCP server integration (the registry is designed for it; wiring comes with the first MCP-backed tool).
- Policy rules such as "auto-approve emails to my partner". Every critical action is approved individually in v1.
- Approving from any channel other than Telegram.
- Delegation (one user approving actions for another).

## Impact

- New packages `internal/tools`, `internal/approval`; changes to `internal/agent` and `internal/channel`.
- New migration: `tool_calls`, `pending_actions`.
- Depends on `add-chat-foundation`.
