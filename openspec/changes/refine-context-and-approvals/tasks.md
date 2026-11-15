## 1. Store: open proposals, turn-scoped superseding, and pending-only reminders

- [x] 1.1 Add `ProposedActions(ctx, user, now)` and variadic `keepIDs ...string` on `SupersedePending` in `internal/store/store.go`, `internal/store/postgres/store.go`, and `internal/testutil/fakes.go`; register `ProposedActions` in `internal/store/postgres/scoping_test.go` (tests: `TestUnscopedCallReturnsError`, `TestProposedActionsScopedAndUnexpired` in `internal/store/postgres/approval_store_test.go`)
- [x] 1.2 Filter `GetReminder` to pending (`delivered_at IS NULL AND cancelled_at IS NULL`) reminders in `internal/store/postgres/tasks.go` and `internal/testutil/tasks_fake.go`, and enforce the 500-rune limit in `FakeStore.AddReminder` (tests: `TestDeliveredOrCancelledReminderNotFoundOnGet` in `internal/store/postgres/reminders_store_test.go` and `TestCancelNonPendingReminderShowsNoSuchReminder` in `internal/reminders/reminders_test.go`)

## 2. Approval gate & tool validation invariants

- [x] 2.1 Add `approval.WithTurn(ctx)` in `internal/approval/approval.go` and pass the current turn's proposed IDs to `SupersedePending` so same-turn proposals stay `proposed` while cross-turn and standalone proposals supersede earlier ones (tests: `TestSameTurnProposalsStayProposed`, `TestNewProposalSupersedesEarlierOfSameTool` in `internal/approval/approval_test.go`)
- [x] 2.2 Add non-blank `text` schema constraints (`minLength: 1`, `pattern: "\\S"`) to `NotifySelfTool.Schema()` in `internal/tools/demo.go` (test: `TestNotifySelfBlankTextFailsValidation` in `internal/tools/demo_test.go`)

## 3. Agent prompt context, data-marking, and entrypoint wiring

- [x] 3.1 Add optional `Location *time.Location` and `Now func() time.Time` to `agent.Options` in `internal/agent/agent.go` and wire `loc` in `cmd/nestor/main.go` and `evals/tasks/tasks_eval_test.go` (tests: `TestClockAndTimezoneIncludedInSystemPrompt` in `internal/agent/agent_test.go`, `TestAgentOptionsComeFromConfig` in `cmd/nestor/main_test.go`)
- [x] 3.2 Mark `"pending approval <id>"` tool results with `tools.MarkData`, wrap `HandleMessage` in `approval.WithTurn`, reconstruct active `ProposedActions` as `assistant` tool calls + marked `tool` results in `Agent.prompt`, and extend `TestStructuralCriticalToolExecution` to scan `cmd/` as well as `internal/` in `internal/agent/agent.go` and `internal/agent/agent_test.go` (tests: `TestToolDispatch`, `TestFailedCardLeavesActionPending`, `TestEditSupersedesAndRequiresNewApproval`, `TestMultipleCriticalCallsInOneTurnStayProposed`, `TestStructuralCriticalToolExecution` in `internal/agent/agent_test.go`)

## 4. Verification

- [x] 4.1 Run `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `go test -count=1 -tags integration ./...`, and `openspec validate --all --strict` to verify all unit, integration, and spec checks pass (tests: full unit and Postgres integration suites)
