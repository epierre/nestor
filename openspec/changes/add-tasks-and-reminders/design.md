# Design: tasks and reminders

## Data model

Migration `0006_tasks_reminders`:

- `tasks(id, user_id, title, due_date, created_at, done_at)`. `due_date` is a calendar day, null when
  the task has none; it is stored as a `date`, so no time zone is attached to it. `done_at` is set by
  `task_complete`. A deleted task is removed; the approval record of the deletion remains in
  `pending_actions`.
- `reminders(id, user_id, text, due_at, chat_id, created_at, delivered_at, cancelled_at)`.
  `chat_id` is the chat the reminder was created in (from the request context, as for card chats).
  A cancelled reminder is kept with `cancelled_at` set and is never delivered.

Both tables carry `user_id` as a required column with an index on `(user_id, …)`, and child rows
reference their user as the existing tables do.

## Tools

| Tool | Risk | Arguments | Effect |
|---|---|---|---|
| `task_add` | reversible | `title` (1–200 chars), `due` (optional `YYYY-MM-DD`, read in `TIMEZONE`) | inserts a task |
| `task_list` | read | `include_done` (bool, default false) | returns open tasks with IDs and due days, earliest due first, undated last |
| `task_complete` | reversible | `id` | sets `done_at` on the user's own task |
| `task_delete` | critical | `id` | deletes the user's own task after approval |
| `reminder_add` | reversible | `text`, `due` (RFC 3339 with offset, or local time read in `TIMEZONE`) | inserts a reminder with the current chat |
| `reminder_list` | read | none | returns pending reminders with IDs and due times |
| `reminder_cancel` | critical | `id` | sets `cancelled_at` after approval |

The model never supplies a user ID. Every lookup by `id` filters on the user in the context, so an
ID that belongs to someone else behaves like an unknown ID.

A `due` that is not a valid calendar day is refused with an error result the model can read.

`task_delete` and `reminder_cancel` pass through `approval.Gate.Propose`. The preview names the
task title or reminder text and due time, read from the stored row, so the owner sees what will be
removed.

## Scheduler

`reminders.Run(ctx, store, notifier, every)` runs at startup and every tick (default 30 s):

1. List the users that have reminders due (`due_at <= now`, not delivered, not cancelled). This
   query returns user IDs only.
2. For each user, with that user in context, fetch their due reminders and send each one to its
   `chat_id` through the notifier (single attempt, as for notices).
3. On a successful send, mark the reminder delivered. On a failed send, leave it for the next tick.

A reminder is therefore delivered at least once. A duplicate is possible only if the send succeeds
and marking it delivered fails; this is logged.

## Time

Reminder times are stored as UTC instants. `TIMEZONE` defaults to `Europe/Zurich` and is loaded with `time.LoadLocation` at startup;
an invalid name stops the process with a message naming the variable. Replies show times in that
zone.

## Interfaces

- Reuses `approval.Notifier` for the scheduler, so the Telegram channel's `Notify` serves reminders
  as it serves notices.
- Tools are registered in the registry in `cmd/nestor`, as the demo tools are; the task and reminder
  tools are always registered, not behind a flag.

## Failure modes

- Telegram down at due time: the reminder stays due and is sent on a later tick, after the
  channel recovers.
- Process down at due time: reminders are sent when the process starts again.
- Invalid `TIMEZONE`: the process does not start.
- A critical action proposed but never approved: nothing is deleted; the reminder or task stays.

## Dependencies

None new. Time zones come from the standard library's embedded tz data (`time/tzdata` import).
