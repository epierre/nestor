## 1. Storage

- [x] 1.1 Migration `0006_tasks_reminders`: `tasks` and `reminders` tables with `user_id` and indexes. Test: `TestMigrateUpAndDown`, `TestUserDataTablesRequireUserID` (integration).
- [x] 1.2 Store methods for tasks: add with optional due day, list ordered by due day with undated last, complete, delete; every method scoped by user. Test: integration `TestTasksScopedToUser`, `TestTaskDueDaysOrderedUndatedLast`, `TestCompleteHidesTaskUnlessDoneRequested`, `TestDeleteTaskRemovesIt`, `TestInvalidTaskValuesRejected`.
- [x] 1.3 Store methods for reminders: add with chat, list pending, cancel, mark delivered, list users with due reminders, list a user's due reminders. Test: integration `TestReminderRecordsItsChat`, `TestDueRemindersPerUser`, `TestMarkDeliveredOnce`, `TestCancelledReminderNeverDue`, `TestReminderScopedToUser`.

## 2. Tools

- [x] 2.1 `task_add`, `task_list`, `task_complete`: argument schemas, risk levels, behaviour, optional due day. Test: `internal/tasks/tasks_test.go` `TestAddValidatesTitleLength`, `TestDueDayStoredAsGiven`, `TestInvalidDueDayRefused`, `TestListSortsByDueDayUndatedLast`, `TestListHidesDoneTasksByDefault`, `TestCompleteOtherUsersTaskRefusedLikeUnknown`, `TestDeleteIsCritical`.
- [x] 2.2 `task_delete` is critical: the proposal names the task; approval deletes it once. Test: `TestDeleteProposesAndDoesNotDeleteUntilApproved`.
- [x] 2.3 `reminder_add` parses `due` in `TIMEZONE` and stores the chat; invalid and past times are refused. Test: `internal/reminders/reminders_test.go` `TestDueReadInConfiguredZone`, `TestPastOrUnparsableTimeRefused`, `TestReminderStoresRequestChat`.
- [x] 2.4 `reminder_list` (read) and `reminder_cancel` (critical, as 2.2). Test: `TestCancelProposesAndCancelsOnceApproved`, `TestReminderListShowsOnlyPending`, `TestReminderToolRisks`.

## 3. Scheduler

- [x] 3.1 `reminders.Run` delivers due reminders per user, marking them delivered only after a successful send. Test: `internal/reminders/scheduler_test.go` `TestDueReminderSentToItsChat`, `TestFailedSendRetriedOnNextTick`, `TestDeliveredReminderNotSentAgain`.
- [x] 3.2 Each user's reminders are read with that user in context. Test: `TestTwoUsersDueAtOnceGetOnlyTheirOwn`.

## 4. Configuration and wiring

- [x] 4.1 `TIMEZONE` config (default `Europe/Zurich`), validated with `time.LoadLocation`. Test: `internal/config/config_test.go` `TestLoadTimezone`.
- [x] 4.2 `cmd/nestor` registers the task and reminder tools always, starts the scheduler, and compose passes `TIMEZONE`. Test: `cmd/nestor/main_test.go` `TestTaskAndReminderToolsAlwaysRegistered`; `docker compose config` valid.

## 5. Verification

- [ ] 5.1 Eval: `evals/tasks/` scenarios: deletion without approval, typed yes, injected task title, another user's task ID, double approval. Compiles under `-tags eval`. Not run yet: it calls real models and costs money; run only when asked.
- [x] 5.2 `openspec validate add-tasks-and-reminders --strict`, `go test ./...`, and the integration suite are green.
