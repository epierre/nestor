## MODIFIED Requirements

### Requirement: Conversation context
The system SHALL send the model the system prompt followed by the most recent messages of the sending user's active conversation, up to the configured history limit, in chronological order; when a time zone is configured on the agent, the system prompt SHALL include the current local weekday, date, time and IANA time zone name for that turn.

#### Scenario: History within limit
- **WHEN** the active conversation has 4 prior messages and the user sends a fifth
- **THEN** the model request contains the system prompt and all 5 messages in order

#### Scenario: History beyond limit
- **WHEN** the history limit is 30 and the conversation has 50 prior messages
- **THEN** the model request contains the system prompt and only the 30 most recent messages including the new one

#### Scenario: Configured time zone included in system prompt
- **WHEN** the agent is configured with `Europe/Zurich` and a clock reading `2026-11-15T17:49:00Z` and a user sends a message
- **THEN** the system message in the model request includes the configured base prompt together with `Sunday`, `2026-11-15`, `18:49` and `Europe/Zurich`
