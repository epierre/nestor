## MODIFIED Requirements

### Requirement: Identity resolution at the boundary
The system SHALL resolve each incoming Telegram sender identity to an internal user ID in the channel, MUST NOT pass platform user identities to the agent, and MAY attach an opaque chat routing address (`channel.WithChat`) to the request context solely so outbound cards, reminders and outcome notices return to the originating chat.

#### Scenario: Agent receives internal ID
- **WHEN** an allowlisted user sends a message
- **THEN** the agent is called with a context carrying the internal user ID and no Telegram user ID

#### Scenario: Originating chat address carried for outbound delivery
- **WHEN** an allowlisted user sends a message in a chat that triggers an approval card
- **THEN** the card's delivery chat address is recorded from the request context without exposing the sender's Telegram user ID to the agent

### Requirement: Start a new conversation
The system SHALL treat the `/new` command as closing the sending user's open conversation without calling the model, so the user's next message opens a fresh conversation with empty history, without affecting other users' conversations.

#### Scenario: User resets the conversation
- **WHEN** a user sends `/new` and then asks "what did I just say?"
- **THEN** `/new` makes no model call, and the model request for the next message contains no messages from before `/new`

#### Scenario: Reset does not affect another user
- **WHEN** user A sends `/new` while user B has an open conversation
- **THEN** user B's next model request still contains B's earlier messages
