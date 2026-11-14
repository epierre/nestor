## ADDED Requirements

### Requirement: Unknown outcomes are reported to their user
The system SHALL report an approved action whose outcome is unknown to its owner: the owner's conversation records the outcome, and the owner's card chat receives a message that names the action's preview and states that the outcome is unknown. The system MUST NOT execute, retry or approve the action as part of reporting it.

#### Scenario: Stale approval reported to its owner
- **WHEN** the sweep fails an approved action whose card chat was recorded
- **THEN** the owner's conversation records that the outcome is unknown, the owner's card chat receives a message naming the preview and stating the outcome is unknown, and the tool is not executed

#### Scenario: Notice not delivered
- **WHEN** sending the notice to the card chat fails
- **THEN** the notice is sent again on the owner's next message, and the action remains failed

#### Scenario: Notice not shown to another user
- **WHEN** another user sends a message while the owner's notice is undelivered
- **THEN** the other user receives nothing about the owner's action
