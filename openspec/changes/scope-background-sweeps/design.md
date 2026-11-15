# Design: per-user background sweeps via `ListActiveUsers`

## Identity directory vs. user-data tables

`users` and `user_identities` establish who may use the host (`SyncAllowlist`, `ResolveIdentity`).
All other tables (`conversations`, `messages`, `model_calls`, `tool_calls`, `pending_actions`,
`tasks`, `reminders`) hold per-user data and MUST only be queried with `store.RequireUser(ctx, user)`
and `WHERE user_id = ...`.

`ListActiveUsers(ctx context.Context) ([]identity.UserID, error)` sits at the same identity
directory boundary as `ResolveIdentity`:

```sql
SELECT u.id
FROM users u
WHERE u.disabled_at IS NULL
  AND EXISTS (
      SELECT 1 FROM user_identities i
      WHERE i.user_id = u.id AND i.disabled_at IS NULL
  )
ORDER BY u.id
```

Because `SyncAllowlist` disables removed entries on `user_identities.disabled_at`, checking both
`u.disabled_at IS NULL` and an enabled `user_identities` row ensures removed allowlist entries
are skipped by background jobs while keeping their stored data intact. If an entry is later
re-added to `ALLOWED_TELEGRAM_USERS`, `cmd/nestor/main.go` runs `SyncAllowlist` before starting
`approval.RunSweeps` and `reminders.Run`, so stalled approvals or due reminders are processed on
startup as soon as the user is active again.

## Per-user sweep loops

1. **`approval.SweepStale`:**
   - Calls `users, err := st.ListActiveUsers(ctx)`.
   - For each `user` in `users`, builds `owner := identity.WithUser(ctx, user)` and calls
     `st.FailStaleApprovals(owner, user, cutoff)`.
   - If failing stale approvals for one user errors, logs the error with `owner` context and
     continues to the remaining active users (returning a joined error or the first error after
     visiting all active users so one broken row never starves another user's sweep).
   - Writes the conversation note and calls `SendOutcomeNotice` under `owner` exactly as today.

2. **`reminders.deliverDue`:**
   - `DueStore` replaces `UsersWithDueReminders(ctx, now)` with `ListActiveUsers(ctx)`.
   - Calls `users, err := st.ListActiveUsers(ctx)`, then for each `user` builds
     `owner := identity.WithUser(ctx, user)` and calls `st.DueReminders(owner, user, now)` and
     `st.MarkReminderDelivered(owner, user, r.ID, now)`.
   - For v1 household scale (one to a handful of users), iterating active users every 30s hits
     the partial index `reminders_due_idx (due_at) WHERE delivered_at IS NULL AND cancelled_at IS NULL`
     (or `user_id` + filter) with negligible load and zero unscoped queries on `reminders`.

## `FakeStore` behaviour in unit tests

Unit tests in `internal/approval`, `internal/agent`, and `internal/reminders` often write rows for
a `UserID` without calling `AddIdentity` first, whereas channel/allowlist tests use `AddIdentity`
and `DisableIdentity`. In `testutil.FakeStore`:
- Recording any user-scoped row (or calling `AddIdentity`) registers that `UserID` as known.
- `DisableIdentity` marks a user disabled when they have no remaining enabled identities in
  `FakeStore.identities`; a subsequent `AddIdentity` re-enables them.
- `ListActiveUsers` returns all known, non-disabled `UserID`s in ascending order.

## Failure modes

- **Database down:** `ListActiveUsers` returns an error; `RunSweeps` and `reminders.Run` log the
  failure and retry on the next tick without crashing the process.
- **Telegram down:** `FailStaleApprovals` still marks stale actions failed and records the
  conversation note per active user; unsent notices stay unnotified for retry on the owner's next
  message, and unsent due reminders remain pending for the next scheduler tick.
- **Per-user store error:** logged against that user's context while other active users in the
  same tick still complete.

## Dependencies

None.
