## MODIFIED Requirements

### Requirement: Edit by conversation
The system SHALL include a user's unexpired proposed actions from the active conversation in model context as assistant tool calls paired with data-marked tool results (never in system messages) so the user can request changes in chat; a replacement proposed in a later turn SHALL mark earlier turns' open proposals of that tool superseded, while multiple critical calls of the same tool in one turn SHALL all remain proposed.

#### Scenario: User asks for a change
- **WHEN** an action with text "Dentist at 3pm" is proposed and its user replies "make it 4pm" in a subsequent turn
- **THEN** the model request for "make it 4pm" includes the earlier proposed tool call and a data-marked pending-approval tool result without placing "Dentist at 3pm" in any system message, a new proposed action with the updated text and a new card exist, the original is superseded, and neither has executed

#### Scenario: Multiple critical calls in one turn stay proposed
- **WHEN** the model calls `notify_self` twice in a single user turn with two distinct messages
- **THEN** both pending actions remain in state proposed, an approval card is sent for each, and neither is superseded unless a replacement `notify_self` action is proposed in a later turn
