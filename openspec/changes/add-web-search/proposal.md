# Add grounded web search tool

## Why

Nestor currently has no live web search capability for current events, documentation lookups, or general factual queries outside tasks, reminders, Google Maps, and Google Workspace. Adding a built-in `web_search` tool backed by a single-shot grounded model call through the existing LiteLLM proxy gives Nestor fast, cheap, live web search with citations while isolating untrusted web content from the main conversation and tools.

## What changes

- **Model citation parsing (`internal/model`):** Add provider-agnostic `model.Citation{Title, URL}` and `CompletionResponse.Citations`, decoding optional OpenAI-compatible `choices[0].message.annotations` entries of type `url_citation`.
- **Conversation context propagation (`internal/store`, `internal/agent`):** Add `store.WithConversation` and `store.ConversationFrom`, and attach the active `conversation.ID` to the context in `agent.HandleMessage` so built-in tools that invoke a model can record `model_calls` rows under the active conversation.
- **Built-in `web_search` tool (`internal/websearch`):** Always-registered read-only (`risk: read`) tool taking a single required `query` (1–500 non-whitespace characters, `additionalProperties: false`). Executes a single isolated `model.ModelClient.Complete` call to the configured `WEB_SEARCH_MODEL` alias with a concise search system prompt (anchored with local weekday, date, time, and timezone when configured), **no tools**, and **no conversation history**. Records every attempt in `model_calls` (failing closed if recording fails), appends deduplicated `http`/`https` source citations, caps output at 16 KiB with UTF-8-safe truncation (`\n[truncated: N bytes omitted]` and `Truncated: true`), and returns results to the main agent only as `[data]`-marked tool-role messages.
- **Configuration & deployment (`internal/config`, `cmd/nestor`, `deploy/`):** Add optional `WEB_SEARCH_MODEL` env var (default `"web-search"`). Register `web_search` as an always-registered built-in tool in `cmd/nestor/main.go`. Add a `web-search` alias to `deploy/litellm.yaml` (`gemini/gemini-3.5-flash-lite`, `api_key: os.environ/GEMINI_API_KEY`, `web_search_options: {}`), pass `WEB_SEARCH_MODEL: ${WEB_SEARCH_MODEL:-}` only to `nestor` in `deploy/docker-compose.yml`, and document it in `deploy/.env.example`.

## Capabilities

- `web-search` (new)
- `tool-registry` (modified)
- `observability` (modified)

## Constitution principles touched

- **1. Approval is enforced in code:** `web_search` is defined in Go with `risk: read` (no side effects) and calls an isolated model with zero tools, so web content can never execute or approve any critical action.
- **2. Content from tools is untrusted data:** Web search summaries and cited page titles/URLs enter the main agent conversation only as `[data]`-marked tool-role messages, never concatenated into system or user messages; the search model itself has no tools and no conversation history, preventing indirect prompt injection from escalating privileges.
- **3. Provider-agnostic:** No vendor SDK is added; Nestor calls only its OpenAI-compatible LiteLLM proxy using the configurable `WEB_SEARCH_MODEL` alias (`web-search` by default). Grounding is configured in `deploy/litellm.yaml` (`web_search_options: {}`), so switching to another grounded model or provider is a config edit with zero code changes.
- **4. Personal data stays on the host:** Only the focused `query` string (capped at 500 characters) and the current date/time/timezone are sent to the search model—never conversation history, tasks, reminders, emails, or other user data.
- **5. Everything is observable:** Both the outer `web_search` tool call (`tool_calls`) and every underlying search model attempt (`model_calls` with user ID, conversation ID, search model alias, tokens, cost, latency, and redacted request/response) are recorded in Postgres and logged via structured `slog` with the turn's `request_id` and `user_id` (without logging query or response text).
- **6. User-scoped by design:** `web_search` requires an authenticated `user_id` and `conversation_id` in context, refuses execution without them, and records all calls strictly scoped to that user and conversation.
- **7. Secrets never live in the repo:** Provider keys (`GEMINI_API_KEY`) stay inside the LiteLLM container; Nestor never sees them. Only `WEB_SEARCH_MODEL` (an alias name) is exposed to `nestor`.
- **8. Small, reversible steps:** Thin vertical slice using existing LiteLLM proxy, existing `model.ModelClient` interface, zero new dependencies, and zero database migrations.

## Non-goals

- Multi-turn iterative web research subagent loops or spawning general-purpose subagents.
- Browsing arbitrary websites, fetching arbitrary user-supplied URLs, clicking links, or submitting web forms (explicitly out of scope for v1 in `docs/vision.md`).
- Calling Google GenAI or any third-party SERP API directly from Go (all LLM access stays behind LiteLLM).
- Per-user search budgets, rate limiting, or model-tier routing (belongs to `add-model-routing` / `add-household-users`).
- Caching search results across users or turns.

## Impact

- Packages/files to add or modify: `internal/model`, `internal/store`, `internal/agent`, `internal/websearch` (new), `internal/config`, `cmd/nestor`, `deploy/litellm.yaml`, `deploy/docker-compose.yml`, `deploy/.env.example`, `deploy/compose_test.go`.
- New optional environment variable: `WEB_SEARCH_MODEL` (default `web-search`).
- Zero new external Go dependencies, zero database migrations.
