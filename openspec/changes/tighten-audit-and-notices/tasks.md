# Tasks: tighten audit trail, outcome notices, and schema cleanup

## 1. Schema migration and store audit linkage

- [x] 1.1 Add migration `0007_audit_and_schema_cleanup.{up,down}.sql` (dropping `user_identities.chat_id`, removing `'pending'` from `tool_calls.status`, adding composite `(id, user_id)` foreign keys on `tool_calls.model_call_id` and `pending_actions.tool_call_id`) and update `Store` (`RecordModelCall` and `RecordToolCall` returning `(int64, error)`, `PendingAction.ToolCallID`, `LinkActionToolCall`, `CompleteToolCall`, removing `ToolCallStatusPending`) in `internal/store`, `internal/store/postgres`, and `internal/testutil`. Prove with `TestAuditLinksAndCompleteToolCall` and `TestMigrate0007UpAndDown` in `internal/store/postgres/store_test.go` plus `TestUnscopedCallReturnsError` in `internal/store/postgres/scoping_test.go`.

## 2. Helper cleanup in tools and entrypoint

- [x] 2.1 Remove unused standalone `tools.Validate(t Tool, args)` from `internal/tools/registry.go` and wire `tools.RegisterDemoTools` in `cmd/nestor/main.go`. Prove with `TestDemoToolsOfferedOnlyWhenEnabled` in `cmd/nestor/main_test.go` and `TestRegistry` / `TestDemoTools` in `internal/tools/`.

## 3. Approval execution audit and structured sweep logs

- [x] 3.1 Update `approval.Gate` (`internal/approval/approval.go`) to call `CompleteToolCall` on linked `tool_calls` rows after approved execution or failure, emit structured `slog` entries for critical execution and delivered outcome notices, and attach `obs.WithRequestID` in `approval.SweepStale` and `reminders.deliverDue`. Prove with `TestApproveUpdatesLinkedToolCall` and `TestSweepAndNoticeStructuredLogs` in `internal/approval/approval_test.go`, and `TestDeliverDueLogsRequestID` in `internal/reminders/reminders_test.go`.

## 4. Agent audit linkage, tool logs, and zero-chat notice fallback

- [x] 4.1 Update `Agent` (`internal/agent/agent.go`) to pass `model_call_id` into `dispatchToolCalls`, link proposed actions via `LinkActionToolCall`, emit `"tool call finished"` structured logs without tool arguments/results, and fall back to `channel.ChatFrom(ctx)` in `resendOutcomeNotices` when `NotifyChatID == 0`. Prove with `TestToolCallsLinkedToModelCallAndPendingAction`, `TestToolDispatchStructuredLogOmitsArguments`, and `TestUnrecordedChatNoticeDeliveredOnNextMessage` in `internal/agent/agent_test.go`.

## 5. Verification

- [ ] 5.1 Run `gofmt -l .`, `go vet ./...`, `go test -count=1 ./...`, `go test -count=1 -tags integration ./...`, and `openspec validate --all --strict`.
