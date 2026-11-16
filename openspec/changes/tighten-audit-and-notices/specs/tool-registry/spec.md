## MODIFIED Requirements

### Requirement: Tool call log
The system SHALL record every tool call with user ID, conversation ID, triggering model call ID (when called by a model turn), tool name, arguments, risk level, status, result or error, and latency, and SHALL update a proposed critical call's row when it finishes executing after approval.

#### Scenario: Executed tool recorded
- **WHEN** `current_time` executes
- **THEN** a tool call row exists with risk read, status executed and the result

#### Scenario: Tool call linked to model call and pending action
- **WHEN** a model turn calls a read tool and proposes a critical tool
- **THEN** both `tool_calls` rows record the `model_call_id` of that model turn, and the proposed critical action's `pending_actions` row records the critical `tool_call_id`

#### Scenario: Approved critical execution updates tool call row
- **WHEN** a critical tool call was recorded as `proposed` and its owner approves it so the tool executes
- **THEN** the linked `tool_calls` row is updated to status `executed` with the execution result and latency
