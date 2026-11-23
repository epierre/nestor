## ADDED Requirements

### Requirement: Critical MCP tool previews
The system SHALL generate an MCP tool's approval preview as `"tool <server>__<tool>"`, omitting arguments for empty objects and otherwise rejecting invalid or duplicate-key JSON without echoing content, sorting top-level keys, quoting or escaping unsafe keys, and rendering `key: <json>` lines with HTML escaping disabled and exact numbers preserved.

#### Scenario: Critical MCP tool preview shows sorted arguments
- **WHEN** an allowlisted critical MCP tool `todoist__delete_task` is called with `{"reason": "duplicate & obsolete", "id": 9007199254740993, "meta": {"note": "line1\nline2"}}`
- **THEN** the preview begins with `tool todoist__delete_task`, lists `id: 9007199254740993`, `meta: {"note":"line1\nline2"}`, and `reason: "duplicate & obsolete"` in sorted key order, preserves `9007199254740993` without floating-point rounding, and leaves `&` unescaped

#### Scenario: Critical MCP tool with empty arguments omits argument lines
- **WHEN** an allowlisted critical MCP tool `home__lock_doors` is called with `{}`, empty, or whitespace-only arguments
- **THEN** the preview consists solely of `tool home__lock_doors`

#### Scenario: Unsafe MCP argument keys are quoted and escaped
- **WHEN** a critical MCP tool is called with a top-level argument key containing a newline, a colon, leading/trailing whitespace, or a control/bidirectional character
- **THEN** the preview renders that key in escaped JSON-quoted form on its `key: <json>` line so it cannot forge separate card lines or visual layout

#### Scenario: Malformed or duplicate-key arguments fail MCP preview validation
- **WHEN** `adapterTool.Preview` receives malformed JSON, a non-object root, trailing tokens, or duplicate keys
- **THEN** it returns a validation error without echoing untrusted argument keys or values
