## MODIFIED Requirements

### Requirement: Edit by conversation
The system SHALL keep a user's unexpired proposed actions from the active conversation in model context as assistant tool calls paired with tool-role results marked as data (without adding arguments or previews to system messages) so the user can request changes by replying in the chat; a replacement action proposed in a later turn SHALL mark open proposed actions of that tool from earlier turns superseded and require its own approval, whereas multiple critical calls of the same tool proposed within the same turn SHALL all remain proposed and each require its own approval.

#### Scenario: User asks for a change
- **WHEN** an action with text "Dentist at 3pm" is proposed and its user replies "make it 4pm" in a subsequent turn
- **THEN** the model request for "make it 4pm" includes the earlier proposed tool call and a data-marked pending-approval tool result without placing "Dentist at 3pm" in any system message, a new proposed action with the updated text and a new card exist, the original is superseded, and neither has executed

#### Scenario: Multiple critical calls in one turn stay proposed
- **WHEN** the model calls `notify_self` twice in a single user turn with two distinct messages
- **THEN** both pending actions remain in state proposed, an approval card is sent for each, and neither is superseded unless a replacement `notify_self` action is proposed in a later turn
