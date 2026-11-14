# Design: notices for unknown outcomes

## Chat recorded at card delivery

`pending_actions` gets `notify_chat_id bigint`, set when the card is delivered. The card's chat is
the only chat where the action's user talks to the bot, so it is the right place for notices.
Cards that were never delivered have no chat, and an action can only be approved after its card
was pressed, so an approved action always has one.

The chat ID is read by the agent from the request context, through the chat helpers that move
from `internal/channel/telegram` to `internal/channel`. Telegram still sets them. The agent passes
the ID to the store when it marks the card sent. The agent never sees a Telegram identity.

## Notices

`FailStaleApprovals` changes from returning a count to returning the affected actions: ID, owner
and recorded chat. The sweep then, for each one, with the owner in context:

1. appends the outcome note to the owner's open conversation (same text rules as task 4.2:
   action ID and outcome only),
2. sends the notice to the recorded chat through a `Notifier` the sweep is given,
3. records `outcome_notice_sent_at` when the send succeeded.

Step 1 happens whether or not step 2 works, so the model always learns the outcome.

A failed send (step 2) leaves `outcome_notice_sent_at` empty. On the owner's next message, the
agent lists that owner's unnotified outcomes and sends them to the chat of that message, then marks
them, the same way it re-sends cards (task 4.4).

## Interfaces

- `approval.Notifier`: `Notify(ctx, chatID int64, text string) error`, one attempt. The Telegram
  channel implements it; the sweep must not hold up on Telegram retries.
- Agent options gain `Notices`, the same pattern as `Cards`: it sends outcome notices. It is
  optional; without it, notices are not re-sent.

## Migration

`0005_pending_notices`: adds `notify_chat_id bigint` and `outcome_notice_sent_at timestamptz` to
`pending_actions`. The down migration drops both columns.

## Failure modes

- Sweep runs while the channel is down: notes are written, notices stay unsent, and they are re-sent
  on the owner's next message.
- No chat recorded (should not happen for approved actions): note only, no send, logged.
- Owner never writes again: the notice stays unsent. That is accepted; the conversation note still
  records the outcome for the model.

## Dependencies

None.
