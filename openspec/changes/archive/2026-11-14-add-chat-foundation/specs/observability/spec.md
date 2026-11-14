## Purpose

Makes every model interaction inspectable after the fact: what was sent, what came back,
how long it took and what it cost.

## ADDED Requirements

### Requirement: Model call log
The system SHALL record every model call with user ID, conversation ID, model name, start time, latency, prompt tokens, completion tokens, estimated cost, status, error text, full request and full response.

#### Scenario: Successful call recorded
- **WHEN** a user sends a message and the model replies successfully
- **THEN** one model call row exists with that user's ID, status ok, non-zero latency, token counts from the response usage, and the request and response bodies

#### Scenario: Failed call recorded
- **WHEN** a model call times out
- **THEN** a model call row exists with status error, the timeout as error text, and the request body

### Requirement: Cost estimate
The system SHALL store the cost reported by the proxy for each call when present, and SHALL store null (not zero) when no cost is reported.

#### Scenario: Proxy omits cost
- **WHEN** the proxy response carries no cost information
- **THEN** the call row's cost is null

#### Scenario: Cost per user
- **WHEN** two users have each made model calls
- **THEN** total cost per user can be computed from the call log alone

### Requirement: No secrets in logs
The system MUST NOT write API keys or the Telegram bot token to application logs or to the call log.

#### Scenario: Request logging
- **WHEN** a model request is recorded
- **THEN** the stored request contains no Authorization header or API key value

### Requirement: Structured application logs
The system SHALL emit application logs as structured JSON lines including a request ID and the internal user ID that tie together the channel event, model calls and reply for one user message.

#### Scenario: Trace one message
- **WHEN** a user sends one message that results in one model call
- **THEN** the log lines for receiving, calling the model and replying share the same request ID and user ID
