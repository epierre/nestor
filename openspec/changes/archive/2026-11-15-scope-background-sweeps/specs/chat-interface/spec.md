## MODIFIED Requirements

### Requirement: Allowlisted access
The system SHALL process inbound messages and run background outbound deliveries only for identities active on the configured allowlist, and SHALL ignore messages from any other sender.

#### Scenario: Message from an allowlisted user
- **WHEN** an allowlisted user sends "hello" to the bot
- **THEN** the message is passed to the agent under that user's ID and a reply is sent to that user's chat

#### Scenario: Message from a stranger
- **WHEN** a Telegram user who is not on the allowlist sends a message
- **THEN** no reply is sent, no model call is made, and a warning with the sender ID is logged

#### Scenario: Identity removed from the allowlist
- **WHEN** an identity is removed from the allowlist and the process restarts, then that identity sends a message
- **THEN** the message is ignored and the user's stored data is kept

#### Scenario: Disabled identity receives no background deliveries
- **WHEN** a user has a due reminder or stalled approval, their identity is removed from the allowlist and synced, and background sweeps run
- **THEN** no reminder or outcome notice is sent to that user's chat and their stored rows remain intact
