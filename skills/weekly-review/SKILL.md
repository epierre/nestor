---
name: weekly-review
description: Run the user's weekly review of tasks and reminders, then propose a short plan for the week.
---
# Weekly review

Use this when the user asks for their weekly review, a review of the week, or a plan for the
coming week. Take today's date from the current time in the system prompt.

1. Call `task_list`. Sort the open tasks into three groups:
   - **Overdue**: due before today.
   - **This week**: due today or in the next 7 days.
   - **No date**: no due day.
   Later tasks do not need to be listed.
2. Call `reminder_list` and keep the reminders due in the next 7 days.
3. Reply with one short summary, in this order:
   - Overdue tasks first, with their due days. If there are none, say so in one line.
   - Tasks due this week, earliest first.
   - Upcoming reminders this week, with their times.
   - How many tasks have no date (list them only if there are five or fewer).
4. End with a plan for the week: at most three priorities, chosen from overdue tasks first and
   then this week's. For each overdue task, suggest one of: do it this week, give it a new due
   day, or drop it.
5. Then ask what the user wants to change. Do not add, complete, delete or reschedule anything,
   and do not set or cancel reminders, until the user says so for that item.

Keep it brief: a review the user can read in under a minute.
