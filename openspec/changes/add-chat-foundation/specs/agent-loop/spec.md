## Purpose

Turns a user's message into a reply by calling a language model with the conversation so far,
in a way that is independent of which provider serves the model.

## ADDED Requirements

### Requirement: Conversation context
The system SHALL send the model the system prompt followed by the most recent messages of the sending user's active conversation, up to the configured history limit, in chronological order.

#### Scenario: History within limit
- **WHEN** the active conversation has 4 prior messages and the user sends a fifth
- **THEN** the model request contains the system prompt and all 5 messages in order

#### Scenario: History beyond limit
- **WHEN** the history limit is 30 and the conversation has 50 prior messages
- **THEN** the model request contains the system prompt and only the 30 most recent messages including the new one

### Requirement: Conversation persistence
The system SHALL persist every user message and every assistant reply with its user ID, conversation, role and timestamp before the reply is sent.

#### Scenario: Restart keeps context
- **WHEN** a user sends a message, the process restarts, and the same user sends another message
- **THEN** the second model request includes the first message and its reply

### Requirement: User isolation
The system MUST NOT include any data belonging to one user in a model request, reply or query result made for another user, and every store operation on user data MUST be scoped to a user ID.

#### Scenario: Two users talk at the same time
- **WHEN** user A says "my code word is falcon" and user B then asks "what is my code word?"
- **THEN** user B's model request contains no message from user A

#### Scenario: Unscoped store call
- **WHEN** a store method for user data is called with a context that carries no user ID
- **THEN** it returns an error and performs no query

### Requirement: Provider-agnostic model access
The system SHALL call models only through an OpenAI-compatible chat completions endpoint whose base URL, API key and model name come from configuration.

#### Scenario: Switch model by configuration
- **WHEN** `LLM_MODEL` is changed from one configured model to another and the process restarts
- **THEN** subsequent model calls use the new model name with no code change

### Requirement: Bounded loop
The system MUST stop processing a message after the configured maximum number of model calls and reply with an error notice.

#### Scenario: Runaway loop
- **WHEN** the model keeps returning tool calls and the maximum is 8
- **THEN** exactly 8 model calls are recorded for that message and the user receives an error notice

### Requirement: Model failure handling
The system SHALL reply to the user with a short failure notice when the model call fails or times out, retrying at most once and only for server errors.

#### Scenario: Provider returns 503
- **WHEN** the model endpoint returns HTTP 503 twice
- **THEN** two model calls are recorded with status error and the user receives the failure notice

#### Scenario: Provider returns 400
- **WHEN** the model endpoint returns HTTP 400
- **THEN** one model call is recorded with status error, no retry is made, and the user receives the failure notice

### Requirement: Fail fast on missing configuration
The system MUST refuse to start and exit with a non-zero status when any required configuration value is missing.

#### Scenario: Missing bot token
- **WHEN** the process starts without `TELEGRAM_BOT_TOKEN`
- **THEN** it exits non-zero and logs which variable is missing, without logging any secret values
