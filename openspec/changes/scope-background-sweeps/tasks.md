## 1. Store interface and user scoping

- [x] 1.1 Add `ListActiveUsers(ctx)` to `store.Store`, `postgres.Store`, and `testutil.FakeStore`, returning only non-disabled users with an enabled identity, and remove `UsersWithDueReminders`. Test: integration `internal/store/postgres/store_test.go` `TestListActiveUsersSkipsDisabledIdentities`.
- [x] 1.2 Scope `FailStaleApprovals(ctx, user, cutoff)` to a single user with `store.RequireUser(ctx, user)` and `WHERE user_id = $1` in `postgres.Store` and `testutil.FakeStore`. Test: integration `internal/store/postgres/approval_store_test.go` `TestSweepReturnsStaleRowsWithTheirOwnChats` (updated to assert per-user scoping across two seeded users).
- [ ] 1.3 Extend `internal/store/postgres/scoping_test.go` `TestUnscopedCallReturnsError` to cover every user-data method on `store.Store` (including `FailStaleApprovals`, `DueReminders`, `ListTasks`, `GetReminder`, etc.). Test: `internal/store/postgres/scoping_test.go` `TestUnscopedCallReturnsError`.

## 2. Background sweeps and scheduler

- [ ] 2.1 Update `approval.SweepStale` to iterate `ListActiveUsers`, scope each `FailStaleApprovals` call with `identity.WithUser(ctx, user)`, and skip disabled users while continuing across active users on per-user errors. Tests: `internal/approval/approval_test.go` `TestSweepScopesQueriesPerActiveUser` and `TestSweepSkipsDisabledUser`.
- [ ] 2.2 Update `reminders.DueStore` and `reminders.deliverDue` to use `ListActiveUsers`, fetching and delivering due reminders only for active allowlisted users with each user's ID in context. Tests: `internal/reminders/scheduler_test.go` `TestTwoUsersDueAtOnceScopedPerUser` and `TestDisabledUserReminderNotDelivered`.

## 3. Verification

- [ ] 3.1 Run `openspec validate --all --strict`, `gofmt -l .`, `go vet ./...`, `go test ./...`, and `go test -tags integration ./...` with zero errors. Test: full unit and PostgreSQL integration suites.
