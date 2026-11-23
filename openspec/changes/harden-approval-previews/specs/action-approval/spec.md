## MODIFIED Requirements

### Requirement: Critical calls become pending actions
The system SHALL NOT execute a critical tool call when requested; it SHALL normalize empty arguments to `{}`, escape disallowed control and bidirectional characters, enforce a `3900` UTF-16 code unit preview cap before storing or superseding actions, record `rejected` tool calls with `[data]` errors on recoverable preview/validation rejections while keeping store errors fatal, and otherwise store a pending action with user, tool, arguments, hash, preview, and expiry.

#### Scenario: Model requests a critical action
- **WHEN** the model calls `notify_self` with text "Dentist at 3pm" during user A's conversation
- **THEN** no message is sent by the tool, a pending action in state proposed owned by user A exists, and the model receives a tool result containing the pending action ID

#### Scenario: Empty critical arguments normalized before persistence
- **WHEN** the model calls a critical tool with empty or whitespace-only arguments that pass schema validation
- **THEN** arguments are normalized to `"{}"` before preview generation, `ArgsHash` computation, `pending_actions` persistence, and `tool_calls` logging

#### Scenario: Oversized critical preview rejected without losing prior proposals
- **WHEN** user A has an existing `proposed` pending action and the model proposes a critical tool call whose final escaped preview exceeds `3900` UTF-16 code units
- **THEN** no new pending action is stored, the earlier pending action remains `proposed` (not superseded), a `rejected` `tool_calls` row is recorded, content-free structured telemetry logs the rejection reason and UTF-16 counts, and the model receives a `[data]` tool error naming the measured and maximum `3900` UTF-16 unit counts in the same turn

#### Scenario: Preview at exact 3900 UTF-16 unit cap succeeds
- **WHEN** the model proposes a critical tool call whose final escaped preview measures exactly `3900` UTF-16 code units (whether ASCII or multi-unit surrogate-pair characters)
- **THEN** the pending action is stored and its approval card is queued for delivery

#### Scenario: Escape expansion counted before preview length cap
- **WHEN** a critical tool preview is shorter than `3900` UTF-16 code units before escaping control or bidirectional characters, but exceeds `3900` UTF-16 code units after escaping them to `\uXXXX` sequences
- **THEN** the proposal is rejected with the oversized-preview error before storing or superseding any pending action

#### Scenario: Store failure during proposal aborts turn
- **WHEN** persisting a valid critical proposal fails due to a database or store error
- **THEN** `HandleMessage` aborts with an error instead of returning a `[data]` tool error to the model

### Requirement: Approval card
The system SHALL send the approval card only to the action's owner with Approve and Reject controls and a preview from stored arguments (and owner data when needed), visibly escaping disallowed control and bidirectional characters while preserving `\n` and `U+200D` within a `3900` UTF-16 unit cap so cards and notices fit Telegram's `4096`-unit limit, and SHALL retry undelivered cards on the owner's next message while proposed and unexpired.

#### Scenario: Card shows what will run
- **WHEN** a pending action for `notify_self` with text "Dentist at 3pm" is created for user A
- **THEN** user A receives a card whose text includes "Dentist at 3pm" with Approve and Reject buttons, and no other user receives it

#### Scenario: Preview uses the owner's stored item
- **WHEN** a critical tool builds its preview from the owner's stored data as well as the call arguments
- **THEN** the pending action stores the preview resolved under the owner's user ID in context

#### Scenario: Undelivered card retried on next message
- **WHEN** sending an approval card fails when the action is proposed and the owner sends another message before the action expires
- **THEN** the approval card for that pending action is sent once on the next message and not sent again on later messages

#### Scenario: Bidi and control characters visibly escaped on approval cards
- **WHEN** a critical call's preview contains bidirectional overrides such as `U+202E`, line/paragraph separators, or non-newline control characters alongside intentional newlines (`\n`) and joined emoji (`U+200D`)
- **THEN** the stored preview and approval card visibly escape the disallowed characters as `\uXXXX` sequences while preserving `\n` and `U+200D`

#### Scenario: Maximum-length preview fits all approval card and outcome templates
- **WHEN** a stored pending action has a preview of exactly `3900` UTF-16 code units
- **THEN** the rendered approval card, `OutcomeNoticeText`, and every settled outcome message fit within Telegram's `4096` UTF-16 code unit limit
