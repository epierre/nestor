# observability Specification

## Purpose
Makes every model interaction inspectable after the fact: what was sent, what came back,
how long it took and what it cost.

## Requirements

### Requirement: Model call log
The system SHALL record every model call with user ID, conversation ID, model name, start time, latency, prompt tokens, completion tokens, estimated cost, status, error text, full request and full response.

#### Scenario: Successful call recorded
- **WHEN** a user sends a message and the model replies successfully
- **THEN** one model call row exists with that user's ID, status ok, non-zero latency, token counts from the response usage, and the request and response bodies

#### Scenario: Failed call recorded
- **WHEN** a model call times out
- **THEN** a model call row exists with status error, the timeout as error text, and the request body

#### Scenario: Web search tool model call recorded
- **WHEN** a user message triggers `web_search`
- **THEN** every attempt made by `web_search` is recorded in `model_calls` with the requesting user's ID, the active conversation ID, the configured `WEB_SEARCH_MODEL` alias, latency, tokens, proxy-reported cost (or null), status, and redacted request/response bodies, and structured logs share the turn's `request_id` and `user_id` without logging query or response text

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
The system SHALL emit application logs as structured JSON lines carrying a request ID and, when acting for a user, the internal user ID across channel events, model calls, tool dispatches, outcome notices, and background sweeps or reminder ticks, without logging message text or tool contents.

#### Scenario: Trace one message
- **WHEN** a user sends one message that results in one model call
- **THEN** the log lines for receiving, calling the model and replying share the same request ID and user ID

#### Scenario: Tool dispatch logged without tool contents
- **WHEN** a user message triggers a tool call carrying secret-looking argument text `"top-secret-arg"`
- **THEN** a structured log line `"tool call finished"` is emitted with the same `request_id` and `user_id`, naming the tool, risk, status, and `latency_ms`, and no log line contains `"top-secret-arg"`

#### Scenario: Background sweep and reminder tick carry request ID
- **WHEN** a stale-approval sweep fails an action or a reminder tick delivers a due reminder
- **THEN** the structured log lines emitted during that run carry both a non-empty `request_id` and the owner's `user_id`
