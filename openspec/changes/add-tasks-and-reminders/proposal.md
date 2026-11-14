# Tasks and reminders

## Why

The vision's first real use: the owner adds, lists, completes and deletes tasks by talking to the
assistant, and asks to be reminded of something at a time, receiving the message then. This is the
first change that gives the model tools with real effects on user data, so it exercises the
approval gate for the first time against user data rather than a demo.

## What changes

- Task tools: `task_add` and `task_complete` (reversible), `task_list` (read), `task_delete`
  (critical: deleting data is approval-gated, as the vision requires).
- Reminder tools: `reminder_add` (reversible), `reminder_list` (read), `reminder_cancel`
  (critical).
- A scheduler, running in the entrypoint, that delivers each reminder when it is due: the text is
  sent to the chat the reminder was created in, then the reminder is marked delivered. A failed send
  is retried at the next tick.
- A `TIMEZONE` setting (IANA name, default `UTC`) used to read and show reminder times.

## Depends on

- `add-tool-approval-gate` and `add-stale-approval-notices` (both merged; their specs are in
  `openspec/specs/`).

## Capabilities

- `tasks` (new)
- `reminders` (new)

## Constitution principles touched

- 1 Approval enforced in code: deleting a task and cancelling a reminder are critical tools and
  go through the gate. Nothing else changes approval.
- 2 Untrusted content: task and reminder text is user data, stored and shown; it never becomes
  an instruction to the agent beyond the tool's arguments.
- 6 User-scoped by design: every task and reminder row belongs to one user. The scheduler first
  lists which users have due reminders, then reads and delivers each user's reminders with that
  user in context. No query returns rows of two users.
- 4 Personal data stays on the host: reminder text is sent only to the owner's own chat.

## Non-goals

- Recurring reminders and repeating tasks.
- Linking reminders to tasks.
- Due dates on tasks (tasks have no date in this change).
- Calendar integration (a later change).
