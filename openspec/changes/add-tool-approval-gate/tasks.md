## 1. Registry

- [ ] 1.1 Define `Tool`, `Risk`, `Result` and an in-memory `Registry` with lookup and model-facing tool list. Test: `registry_test.go` "lists only registered tools", "unknown tool lookup fails".
- [ ] 1.2 JSON Schema argument validation. Test: `registry_test.go` "missing required arg returns validation error".
- [ ] 1.3 Demo tools `current_time` (read) and `notify_self` (critical) behind `ENABLE_DEMO_TOOLS`. Test: `demo_test.go` for each tool's Execute and Preview.

## 2. Agent integration (non-critical path first)

- [ ] 2.1 Agent loop dispatches tool calls through the registry and appends results as marked tool messages. Test: `agent_test.go` "read tool executes and result returned", "unknown tool yields error result".
- [ ] 2.2 Migration for `tool_calls` with user_id; record every call; tools execute with the user ID in context. Test: integration `store_test.go` "tool call recorded with user", `agent_test.go` "tool receives requesting user in context", "user_id argument rejected by schema".
- [ ] 2.3 Agent holds a registry view that refuses to execute critical tools. Test: `agent_test.go` "critical tool never executed by agent", plus a structural test asserting `Execute` on critical tools is only called from `internal/approval`.

## 3. Pending actions

- [ ] 3.1 Migration for `pending_actions` with user_id and random IDs; `approval.Propose` stores args, hash over user+tool+args, preview, expiry. Test: integration `approval_store_test.go` "propose stores hash and expiry", "IDs not sequential".
- [ ] 3.2 Critical tool calls go to `Propose`; model receives "pending approval <id>". Test: `agent_test.go` "critical call becomes pending", "risk claim in args ignored".
- [ ] 3.3 `approval.Decide(userID, actionID, decision)` loading the action scoped by user ID, with atomic state transition, expiry check, hash check. Test: `approval_test.go` "approve executes once", "double tap executes once", "expired not executed", "tampered args refused", "other user's approval behaves like unknown action", "stranger callback ignored".

## 4. Telegram flow

- [ ] 4.1 Approval card with inline buttons sent to the action's user only; callback queries resolved to a user ID and routed to `Decide`. Test: `telegram_test.go` "card contains preview and buttons", "card sent only to action's user", "callback reaches Decide with resolved user and action ID".
- [ ] 4.2 Outcome and rejection messages posted to chat and appended to conversation. Test: `agent_test.go` "rejection visible to model on next turn".
- [ ] 4.3 Edit by conversation: pending actions in context, replacement supersedes original. Test: `agent_test.go` "edit supersedes and requires new approval" (fake model scripted to propose replacement).
- [ ] 4.4 Re-send cards for a user's still-pending actions when that user next messages after a send failure. Test: `telegram_test.go` "pending cards re-sent".

## 5. Evals

- [ ] 5.1 `evals/approval/` scenarios: typed "yes", model claims approval, injection in tool result, risk claim in args, double approval, cross-user approval attempt (two seeded users). Runner executes against a real model via the proxy and asserts zero unapproved critical executions. Test: `go test -tags eval ./evals/...` passes against both the `cheap` and `strong` aliases.
- [ ] 5.2 Run `openspec validate add-tool-approval-gate --strict` and `go test ./...` green.
