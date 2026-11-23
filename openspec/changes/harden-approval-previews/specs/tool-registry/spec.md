## MODIFIED Requirements

### Requirement: Argument validation
The system SHALL normalize empty or whitespace-only arguments to `{}`, SHALL enforce strict JSON object validation on all tool calls (object root, no malformed JSON, no trailing tokens, and no duplicate keys at any nesting depth) even when no schema is compiled without echoing argument content, SHALL validate against the tool's schema when present, and SHALL return validation errors to the model.

#### Scenario: Invalid arguments
- **WHEN** the model calls `notify_self` without the required `text` argument
- **THEN** nothing is executed or proposed and the model receives a tool result describing the validation error

#### Scenario: Blank notification text rejected
- **WHEN** the model calls `notify_self` with an empty or whitespace-only `text` argument
- **THEN** schema validation rejects the call before proposal, no pending action is created, and the model receives a tool result describing the validation error

#### Scenario: Duplicate JSON keys rejected at any depth
- **WHEN** the model calls a tool with duplicate JSON keys at the top level, inside a nested object, inside an object within an array, or via Unicode-escaped key equivalents such as `"id"` and `"\u0069d"`
- **THEN** validation fails before proposal or execution, nothing is executed or proposed, and the validation error does not echo the untrusted argument keys or values

#### Scenario: Non-object root and trailing tokens rejected even without a schema
- **WHEN** a registered tool has no compiled JSON schema and the model calls it with a non-object JSON root (such as `[]`, `"text"`, or `null`) or with trailing tokens after a JSON object (such as `{}{}`)
- **THEN** validation rejects the call before execution or proposal without echoing untrusted argument content

#### Scenario: Empty or whitespace arguments normalized to empty object
- **WHEN** the model calls a zero-argument tool whose schema accepts an empty object with empty or whitespace-only arguments
- **THEN** arguments are normalized to `{}` before validation, execution, proposal, and tool-call logging
