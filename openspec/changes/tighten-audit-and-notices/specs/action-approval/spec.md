## MODIFIED Requirements

### Requirement: Approval audit trail
The system SHALL record for each pending action its user, triggering tool call ID (when proposed from a model tool call), proposal time, decision, decision time, execution time, result or error, and any superseding action.

#### Scenario: Audit after execution
- **WHEN** an action has been approved and executed
- **THEN** its row shows its user ID and state executed with proposal, decision and execution timestamps and the result

#### Scenario: Pending action linked to tool call
- **WHEN** the agent proposes a critical tool call during a user's conversation
- **THEN** the stored `pending_actions` row has `tool_call_id` set to that user's `proposed` `tool_calls` row ID

### Requirement: Unknown outcomes are reported to their user
The system SHALL report an approved action whose outcome is unknown to its owner: the owner's conversation records the outcome, and the owner's card chat (or, when none was recorded, the chat of the owner's next message) receives a notice naming the preview and stating the outcome is unknown, without executing or retrying the action.

#### Scenario: Stale approval reported to its owner
- **WHEN** the sweep fails an approved action whose card chat was recorded
- **THEN** the owner's conversation records that the outcome is unknown, the owner's card chat receives a message naming the preview and stating the outcome is unknown, and the tool is not executed

#### Scenario: Notice not delivered
- **WHEN** sending the notice to the card chat fails
- **THEN** the notice is sent again on the owner's next message, and the action remains failed

#### Scenario: Notice delivered on next message when card chat was unrecorded
- **WHEN** an approved action with no recorded card chat (`notify_chat_id = 0`) is failed as outcome unknown and its owner later sends a message from a chat
- **THEN** the outcome-unknown notice is sent once to the chat of that message, marked delivered, and not sent again on subsequent messages

#### Scenario: Notice not shown to another user
- **WHEN** another user sends a message while the owner's notice is undelivered
- **THEN** the other user receives nothing about the owner's action
