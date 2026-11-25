# web-search Specification

## Purpose

Gives Nestor a fast, isolated, provider-agnostic `web_search` tool backed by a grounded model call through the configured LLM proxy.

## ADDED Requirements

### Requirement: Web search tool
The system SHALL register a built-in read-only (`risk: read`) tool named `web_search` that accepts a single required non-whitespace `query` string up to 500 characters and executes one isolated completion request (with no tools and no conversation history) against the configured web search model alias, returning the result to the main conversation only as a `[data]`-marked tool-role message capped at 16384 bytes.

#### Scenario: Isolated grounded search execution
- **WHEN** a user sends a message that triggers `web_search` with `{"query": "latest Go release"}`
- **THEN** `web_search` sends one completion request to the configured `WEB_SEARCH_MODEL` alias containing only a search system prompt (with the configured local date, time, and timezone when set) and the user query, with no tools and no conversation history, and returns the result to the main agent as a `[data]`-marked tool-role message

#### Scenario: Blank or oversized query rejected
- **WHEN** the main model calls `web_search` with a blank query, a query exceeding 500 characters, or an unexpected argument such as `user_id`
- **THEN** validation rejects the call before any search model request is made and returns a tool error result

#### Scenario: Large search output truncated safely
- **WHEN** the search model response plus formatted citations exceeds 16384 bytes (including multi-byte UTF-8 text)
- **THEN** `web_search` truncates the output on a valid UTF-8 rune boundary to fit within the limit with `\n[truncated: N bytes omitted]` and records the tool call with status `truncated`

#### Scenario: Search model failure returns tool error
- **WHEN** the search model call times out, fails with an HTTP error, returns empty text, or returns tool calls instead of an answer
- **THEN** `web_search` returns a tool error result without executing any tool calls returned by the search model, records `tool_calls` with status `error`, and lets the main agent reply cleanly

#### Scenario: Missing user or conversation context rejected
- **WHEN** `web_search` is invoked without an authenticated user ID or without an active conversation ID in context
- **THEN** execution fails immediately with an error and makes no model call

### Requirement: Web search response citations
The system SHALL parse optional OpenAI-compatible `url_citation` annotations from the model response into `Citations`, filter to `http` and `https` URLs, sanitize titles, deduplicate by URL, and append them as a `Sources:` section to the `web_search` tool result.

#### Scenario: OpenAI url_citation annotations appended as sources
- **WHEN** the search model response includes `choices[0].message.annotations` containing duplicate `url_citation` entries, a non-HTTP(S) URL (such as `javascript:alert(1)`), and a title containing newline or control characters
- **THEN** `web_search` deduplicates valid `http` and `https` citations in order, strips control characters from titles, excludes non-HTTP(S) URLs, and appends a `Sources:` section to the tool output

#### Scenario: Missing or non-citation annotations handled cleanly
- **WHEN** the search model response omits `annotations` or includes annotations whose `type` is not `url_citation`
- **THEN** the model client decodes the response without error and `web_search` returns the trimmed response text without a `Sources:` section

### Requirement: Web search model configuration
The system SHALL configure the web search model alias via the `WEB_SEARCH_MODEL` environment variable (defaulting to `web-search`), define a `web-search` grounded model entry in `deploy/litellm.yaml`, and pass `WEB_SEARCH_MODEL` only to the `nestor` service in `deploy/docker-compose.yml`.

#### Scenario: Default web search model alias
- **WHEN** `WEB_SEARCH_MODEL` is unset or empty at startup
- **THEN** configuration loads `WebSearchModel` as `web-search`, and `deploy/litellm.yaml` defines `web-search` with `web_search_options` enabled

#### Scenario: Override web search model alias by configuration
- **WHEN** `WEB_SEARCH_MODEL` is set to `custom-search` and Nestor starts
- **THEN** `web_search` sends its model requests to `custom-search` with no code change

#### Scenario: Compose scopes WEB_SEARCH_MODEL to nestor
- **WHEN** `deploy/docker-compose.yml` is inspected
- **THEN** `WEB_SEARCH_MODEL` is passed only to the `nestor` service and not to any other service
