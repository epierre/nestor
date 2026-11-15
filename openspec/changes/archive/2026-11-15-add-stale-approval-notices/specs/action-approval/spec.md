## MODIFIED Requirements

### Requirement: Stale approvals are failed, never retried
The system SHALL move an action still in state approved, whose approval is older than the configured stale-approval threshold, to state failed with an outcome-unknown error, at startup and periodically. The system MUST NOT execute such an action again.

#### Scenario: Crash after claim
- **WHEN** an action has been claimed as approved, the process stops before its outcome is recorded, and the sweep runs after the threshold has passed
- **THEN** the action's state becomes failed with an error stating the outcome is unknown, the tool is not executed, and the owner's next conversation shows the action as failed

#### Scenario: Recorded outcome lost after execution
- **WHEN** the tool ran but recording executed failed, leaving the action approved, and the sweep runs after the threshold has passed
- **THEN** the action's state becomes failed with an error stating the outcome is unknown, and the tool is not executed again

#### Scenario: Fresh approval left alone
- **WHEN** an action has been approved less than the threshold ago and the sweep runs
- **THEN** the action's state is unchanged and nothing is executed

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
