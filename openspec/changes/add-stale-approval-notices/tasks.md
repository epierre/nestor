## 1. Storage

- [ ] 1.1 Migration `0005_pending_notices`: `notify_chat_id` and `outcome_notice_sent_at` on `pending_actions`. Test: existing `TestMigrateUpAndDown` plus integration `approval_store_test.go` "card delivery records its chat".
- [ ] 1.2 `MarkCardSent` records the chat; `FailStaleApprovals` returns the stale actions with owner and chat instead of a count. Test: integration `approval_store_test.go` "sweep returns stale rows with their card chat", "stale rows of other users returned with their own chats".
- [ ] 1.3 `UnnotifiedOutcomes(ctx, user)` lists the owner's failed-as-unknown actions without a sent notice; `MarkOutcomeNoticed` records a sent one. Test: integration "unnotified outcomes listed per user until marked".

## 2. Channel boundary

- [ ] 2.1 Move the chat-in-context helpers to `internal/channel` (`WithChat`, `ChatFrom`); Telegram uses them. Test: existing `telegram_test.go` tests still pass unchanged.
- [ ] 2.2 The agent records the card's chat when it marks a card sent. Test: `agent_test.go` "card chat recorded on delivery".

## 3. Sweep notices

- [ ] 3.1 The sweep writes the outcome note and sends the notice to the recorded chat. Test: `approval_test.go` "stale approval notice sent to card chat", "stale approval note recorded in conversation".
- [ ] 3.2 A failed notice is kept and sent on the owner's next message. Test: `agent_test.go` "outcome notice re-sent on next message".
- [ ] 3.3 An action with no recorded chat gets its note only. Test: `approval_test.go` "no chat recorded: note only, no send".
- [ ] 3.4 Another user's message never receives this user's notice. Test: `agent_test.go` "notice not shown to another user".

## 4. Verification

- [ ] 4.1 `openspec validate add-stale-approval-notices --strict`, `go test ./...`, and the integration suite are green.
