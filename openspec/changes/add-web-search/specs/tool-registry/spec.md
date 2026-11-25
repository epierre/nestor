## MODIFIED Requirements

### Requirement: Tool declaration
The system SHALL expose to the model only tools registered in the tool registry, each with a name, description, argument schema and a risk level of read, reversible or critical defined in code; built-in task, reminder, and `web_search` (`risk: read`) tools SHALL always be registered, while demo tools SHALL be registered only when `ENABLE_DEMO_TOOLS` is enabled.

#### Scenario: Tools offered to the model
- **WHEN** `ENABLE_DEMO_TOOLS` is enabled and a user sends a message
- **THEN** the model request lists the built-in task, reminder, and `web_search` tools together with `current_time` and `notify_self` with their schemas, and no unregistered tools

#### Scenario: Built-in tools registered by default
- **WHEN** `ENABLE_DEMO_TOOLS` is disabled
- **THEN** the registry contains the built-in task, reminder, and `web_search` tools (`task_add`, `task_list`, `task_complete`, `task_delete`, `reminder_add`, `reminder_list`, `reminder_cancel`, `web_search`) and neither `current_time` nor `notify_self`
