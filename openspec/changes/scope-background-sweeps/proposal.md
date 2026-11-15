# Scope background sweeps per active user

## Why

Constitution principle 6 and `agent-loop` (`User isolation`) require every query on user-data
tables (`pending_actions`, `reminders`, etc.) to take a `user_id` explicitly and call
`store.RequireUser`, and require serving only allowlisted identities. Today two background store
methods bypass that rule: `FailStaleApprovals` updates `pending_actions` across all users without
a `user_id`, and `UsersWithDueReminders` queries `reminders` across all users without checking
whether each user still has an enabled allowlist identity. Removing an identity from
`ALLOWED_TELEGRAM_USERS` therefore stops inbound messages yet still delivers due reminders and
stale-approval notices in the background.

## What changes

- Add `ListActiveUsers(ctx context.Context) ([]identity.UserID, error)` to `store.Store`,
  returning enabled users that have at least one enabled `user_identities` row. Like
  `SyncAllowlist` and `ResolveIdentity`, it queries only the identity directory (`users` and
  `user_identities`), never user-content tables.
- Change `FailStaleApprovals` on `store.Store` to
  `FailStaleApprovals(ctx context.Context, user identity.UserID, cutoff time.Time) ([]FailedApproval, error)`,
  guarded by `store.RequireUser(ctx, user)` and `WHERE user_id = $1`.
- Remove `UsersWithDueReminders` from `store.Store` and `reminders.DueStore`; have both
  `approval.SweepStale` and `reminders.deliverDue` call `ListActiveUsers` first and then query
  each active user's rows with `identity.WithUser(ctx, user)`.
- Extend `TestUnscopedCallReturnsError` in `internal/store/postgres/scoping_test.go` to cover
  every user-data method on `store.Store`.

## Capabilities

- `agent-loop` (modified: `User isolation` extended with per-user background sweep scenario)
- `chat-interface` (modified: `Allowlisted access` extended so disabled identities receive no
  background deliveries)
- `reminders` (modified: `Reminders are user-scoped in delivery` updated to list active users
  from the identity directory instead of querying `reminders` unscoped)

## Constitution principles touched

- 1 Approval enforced in code: upheld. Stale approvals are still failed, never retried or
  executed; if a removed user is later re-allowlisted, the startup sweep runs immediately after
  `SyncAllowlist` and fails any old stalled approval before traffic is served.
- 5 Observable: upheld. Per-user sweep errors and stale-approval counts remain logged with user
  context where applicable.
- 6 User-scoped by design: restored in full. Zero queries touch `pending_actions` or `reminders`
  without `store.RequireUser(ctx, user)` and `WHERE user_id = $1`, and disabled identities are
  excluded from background outbound sends.

## Non-goals

- Schema migrations (existing indexes on `pending_actions(user_id, state)` and
  `reminders(user_id, due_at)` already cover the per-user queries).
- Persisting pending-action proposals across turns, injecting clock/timezone into the system
  prompt, or changing `tool_calls` logging (handled in separate changes).

## Impact

- Updates `internal/store`, `internal/store/postgres`, `internal/testutil`, `internal/approval`,
  and `internal/reminders`. No new dependencies or migrations.
