## ADDED Requirements

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
