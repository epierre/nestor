# agent-loop Specification

## Purpose
Turns a user's message into a reply by calling a language model with the conversation so far,
in a way that is independent of which provider serves the model.

## Requirements

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

### Requirement: Conversation persistence
The system SHALL persist every user message and every assistant reply with its user ID, conversation, role and timestamp before the reply is sent.

#### Scenario: Restart keeps context
- **WHEN** a user sends a message, the process restarts, and the same user sends another message
- **THEN** the second model request includes the first message and its reply

### Requirement: User isolation
The system MUST NOT include any data belonging to one user in a model request, reply or query result made for another user, and every store operation on user data MUST be scoped to a user ID; background sweeps SHALL list active users from the identity directory first and invoke user-data store operations separately for each user.

#### Scenario: Two users talk at the same time
- **WHEN** user A says "my code word is falcon" and user B then asks "what is my code word?"
- **THEN** user B's model request contains no message from user A

#### Scenario: Unscoped store call
- **WHEN** a store method for user data is called with a context that carries no user ID
- **THEN** it returns an error and performs no query

#### Scenario: Background sweep scopes user-table queries per user
- **WHEN** the stale-approval sweep or reminder scheduler runs with two active users
- **THEN** it lists active users first and calls user-data store methods separately for each user with only that user's ID in context

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

### Requirement: Base system prompt file
The assistant daemon SHALL load its base system prompt at startup from the regular UTF-8 file named by `SYSTEM_PROMPT_FILE`, trim whitespace, log the path, raw byte length and SHA-256 digest without logging prompt text, and use it in every model request. Administrative CLI subcommands (`credentials`, `mcp`, `skills`) SHALL NOT require `SYSTEM_PROMPT_FILE`.

#### Scenario: Valid system prompt file loaded at startup
- **WHEN** `SYSTEM_PROMPT_FILE` points to a regular UTF-8 file of at most 4096 bytes containing non-whitespace text and a user sends a message
- **THEN** startup logs the file path, raw byte length and `sha256`-prefixed digest without logging the prompt text, and the system message in the model request begins with the file's trimmed text

#### Scenario: Administrative subcommands run without system prompt file
- **WHEN** `nestor credentials`, `nestor mcp`, or `nestor skills` is invoked with `SYSTEM_PROMPT_FILE` unset
- **THEN** configuration loading succeeds without reading a system prompt file

### Requirement: Fail fast on invalid system prompt file
The assistant daemon MUST refuse to start and exit with a non-zero status before serving any message when `SYSTEM_PROMPT_FILE` is unset or points to a missing, non-regular, unreadable, empty, non-UTF-8, NUL-containing, or oversized (>4096 bytes) file.

#### Scenario: Unset or unreadable system prompt file stops startup
- **WHEN** the assistant daemon starts with `SYSTEM_PROMPT_FILE` unset or pointing to a nonexistent or non-regular path
- **THEN** the process exits with a non-zero status and logs an error naming `SYSTEM_PROMPT_FILE` without logging any secret values, before serving any message

#### Scenario: Empty, binary, or oversized system prompt file stops startup
- **WHEN** `SYSTEM_PROMPT_FILE` points to a file that is whitespace-only, contains invalid UTF-8 or a NUL byte, or exceeds 4096 bytes
- **THEN** prompt loading returns an error and the assistant daemon exits with a non-zero status before serving any message
