## ADDED Requirements

### Requirement: Shared host credentials
The system SHALL support MCP servers with `credentials: shared` over HTTP by reading a host-wide secret from the configured `credential_env` environment variable, sending it in `credential_header` (or `Authorization: Bearer <secret>` when `credential_header` is omitted), requiring `https://` when `remote` is true, and disabling only that server without failing startup when the environment variable is unset or empty.

#### Scenario: Shared server sends custom header
- **WHEN** `mcp.yaml` configures an HTTP server with `credentials: shared`, `credential_env: GOOGLE_MAPS_API_KEY`, `credential_header: X-Goog-Api-Key`, and `GOOGLE_MAPS_API_KEY` is set in the host environment
- **THEN** Nestor sends `X-Goog-Api-Key: <secret>` without an `Authorization` header on every MCP HTTP request to that server, and never writes the secret value to logs or errors

#### Scenario: Shared server defaults to Bearer authorization
- **WHEN** `mcp.yaml` configures an HTTP server with `credentials: shared`, `credential_env: EXAMPLE_TOKEN`, and omits `credential_header`, and `EXAMPLE_TOKEN` is set in the host environment
- **THEN** Nestor sends `Authorization: Bearer <secret>` on every MCP HTTP request to that server

#### Scenario: Missing shared credential env var disables only that server
- **WHEN** `mcp.yaml` configures a `credentials: shared` server whose `credential_env` variable is unset or empty at startup alongside another reachable server
- **THEN** Nestor logs a warning naming the server and environment variable name without any secret value, does not start or register tools for that server, and starts the remaining servers normally

#### Scenario: Invalid shared credential configuration fails startup
- **WHEN** `mcp.yaml` configures `credentials: shared` without a valid `credential_env` name, with `transport: stdio`, with `credential_key` or `credential_delivery` set, with an invalid `credential_header` token, or with `remote: true` and an `http://` URL
- **THEN** configuration validation fails at startup naming the server without contacting any server

### Requirement: Configurable server health interval
The system SHALL accept an optional positive `health_interval` duration per server in `mcp.yaml` to control how often the server supervisor pings and refreshes tool pins, defaulting to 30 seconds when omitted, and MUST reject negative durations at startup.

#### Scenario: Custom health interval overrides default
- **WHEN** `mcp.yaml` configures a server with `health_interval: 5m`
- **THEN** Nestor configures that server's supervisor health check and pin-refresh interval to 5 minutes

#### Scenario: Negative health interval fails startup
- **WHEN** `mcp.yaml` configures a server with a negative `health_interval`
- **THEN** configuration validation fails at startup naming the server

### Requirement: Reviewed Google Maps MCP configuration
The system SHALL ship a reviewed `google-maps` server entry in `config/mcp.yaml` targeting `https://mapstools.googleapis.com/mcp` with `remote: true`, `credentials: shared`, `credential_env: GOOGLE_MAPS_API_KEY`, `credential_header: X-Goog-Api-Key`, `users: ["*"]`, `health_interval: 5m`, and read-only allowlisted tools `search_places`, `lookup_weather`, and `compute_routes`, and SHALL expose `GOOGLE_MAPS_API_KEY` only to the `nestor` service in `deploy/docker-compose.yml`.

#### Scenario: Shipped Google Maps configuration is valid and read-only
- **WHEN** `config/mcp.yaml` is loaded and validated at startup
- **THEN** the `google-maps` entry passes validation with `remote: true`, `credentials: shared`, and `risk: read` on `search_places`, `lookup_weather`, and `compute_routes`

#### Scenario: Shipped configuration starts cleanly without GOOGLE_MAPS_API_KEY
- **WHEN** Nestor starts with the shipped `config/mcp.yaml` and `GOOGLE_MAPS_API_KEY` is empty
- **THEN** startup succeeds, `google-maps` is skipped with a warning, and `timezone` and built-in tools remain available
