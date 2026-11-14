## 1. Storage

- [x] 1.1 Migration `0005_pending_notices`: `notify_chat_id` and `outcome_notice_sent_at` on `pending_actions`. Test: existing `TestMigrateUpAndDown` plus integration `TestCardDeliveryRecordsChat`.
- [x] 1.2 `MarkCardSent` records the chat; `FailStaleApprovals` returns the stale actions with owner and chat instead of a count. Test: integration `TestSweepReturnsStaleRowsWithTheirOwnChats`.
- [x] 1.3 `UnnotifiedOutcomes(ctx, user)` lists the owner's failed-as-unknown actions without a sent notice; `MarkOutcomeNoticed` records a sent one. Test: integration `TestUnnotifiedOutcomesPerUserUntilMarked`.

## 2. Channel boundary

- [x] 2.1 Move the chat-in-context helpers to `internal/channel` (`WithChat`, `ChatFrom`); Telegram uses them. Test: existing `telegram_test.go` tests pass unchanged apart from the helper's package.
- [x] 2.2 The agent records the card's chat when it marks a card sent. Test: `agent_test.go` `TestCardChatRecordedOnDelivery`.

## 3. Sweep notices

- [x] 3.1 The sweep writes the outcome note and sends the notice to the recorded chat. Test: `approval_test.go` `TestStaleApprovalNoticeSentToCardChat`.
- [x] 3.2 A failed notice is kept and sent on the owner's next message. Test: `agent_test.go` `TestOutcomeNoticeResentOnNextMessage`, and `approval_test.go` `TestFailedNoticeStaysUnnotified`.
- [x] 3.3 An action with no recorded chat gets its note only. Test: `approval_test.go` `TestNoChatRecordedGetsNoteOnly`.
- [x] 3.4 Another user's message never receives this user's notice. Test: `agent_test.go` `TestNoticeNotShownToAnotherUser`.

## 4. Verification

- [x] 4.1 `openspec validate add-stale-approval-notices --strict`, `go test ./...`, and the integration suite are green.
