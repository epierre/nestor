## MODIFIED Requirements

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
