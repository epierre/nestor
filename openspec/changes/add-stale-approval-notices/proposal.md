# Tell users about actions whose outcome is unknown

## Why

An approved action is recorded as approved before its tool runs. If the process stops or a write
fails in between, the stale-approval sweep (add-tool-approval-gate, task 3.4) marks the action
failed as "outcome unknown". Today nobody is told: the user already saw "Approved." and then
silence, and the model never learns the outcome. The user may ask for the action again, or
assume it ran.

## What changes

- When a card is delivered, the chat it went to is recorded on the pending action. Only that chat
  is ever used for that action's notices.
- When the sweep fails a stale approval, it writes an outcome note into the owner's conversation
  and sends the owner a message in the recorded card chat: the preview, and that the outcome is
  unknown and nothing will be retried.
- If that message cannot be sent, it is kept and sent again on the owner's next message.
- Nothing about one user's action is ever sent to another user's chat.

## Depends on

- `add-tool-approval-gate` must be archived first, so that the `action-approval` capability exists
  in `openspec/specs/` for this delta to extend.

## Capabilities

- `action-approval` (modified: one requirement added)

## Constitution principles touched

- 1 Approval enforced in code: no change. Notices never execute or approve anything.
- 6 User-scoped by design: the sweep lists stale actions across users, as already approved. Each
  notice and note is then written with that action's user ID in context. Chats are addresses
  recorded per action; no user's data is combined with another's.
- 5 Observable: each notice attempt is logged.

## Non-goals

- Retrying the action. Unknown outcomes are never retried.
- Checking afterwards whether the tool actually ran. Some tools cannot report that.
- Notices for pending actions that expired without a decision. Those have no outcome to report.
