## MODIFIED Requirements

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
