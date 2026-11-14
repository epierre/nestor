## 1. Storage

- [ ] 1.1 Migration `0006_tasks_reminders`: `tasks` and `reminders` tables with `user_id` and indexes. Test: existing `TestMigrateUpAndDown` plus integration `TestUserDataTablesRequireUserID` extended to the new tables.
- [ ] 1.2 Store methods for tasks: add, list open or all, complete, delete; every method scoped by user. Test: integration `tasks_store_test.go` "tasks scoped to user", "complete and delete only own task".
- [ ] 1.3 Store methods for reminders: add with chat, list pending, cancel, mark delivered, list users with due reminders, list a user's due reminders. Test: integration `reminders_store_test.go` "due reminders listed per user", "cancelled reminder never delivered", "delivered once marked".

## 2. Tools

- [ ] 2.1 `task_add`, `task_list`, `task_complete`: argument schemas, risk levels, behaviour. Test: `tasks_test.go` "add validates title length", "list hides done tasks by default", "complete other user's task refused like unknown".
- [ ] 2.2 `task_delete` is critical: a proposal shows the title; approval deletes it once. Test: `tasks_test.go` "delete proposes, does not delete until approved", "approve deletes once".
- [ ] 2.3 `reminder_add` parses `due` in `TIMEZONE` and stores the chat; invalid times are refused. Test: `reminders_test.go` "due read in configured zone", "past or unparsable time refused", "reminder stores request chat".
- [ ] 2.4 `reminder_list` (read) and `reminder_cancel` (critical, same pattern as 2.2). Test: `reminders_test.go` "cancel proposes, cancelled not delivered".

## 3. Scheduler

- [ ] 3.1 `reminders.Run` delivers due reminders per user and marks them delivered only after a successful send. Test: `scheduler_test.go` "due reminder sent to its chat", "failed send retried next tick", "delivered reminder not sent again".
- [ ] 3.2 Each user's reminders are fetched with that user in context. Test: `scheduler_test.go` "two users due at once each get only their own".

## 4. Configuration and wiring

- [ ] 4.1 `TIMEZONE` config (default `UTC`), loaded with `time.LoadLocation`. Test: `config_test.go` "timezone default UTC", "invalid timezone names the variable".
- [ ] 4.2 `cmd/nestor` registers the task and reminder tools and starts the scheduler; compose passes `TIMEZONE` to the container. Test: `main_test.go` "task and reminder tools always registered"; `docker compose config` valid.

## 5. Verification

- [ ] 5.1 Eval: `evals/tasks/` scenarios, owner adds a task then asks for a reminder, deletion requires approval. Run only when asked (costs money).
- [ ] 5.2 `openspec validate add-tasks-and-reminders --strict`, `go test ./...`, integration suite green.
