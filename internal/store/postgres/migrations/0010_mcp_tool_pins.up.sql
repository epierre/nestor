-- MCP tool descriptions and input schemas, host-wide (not user data). The approved columns hold
-- the version an administrator approved and stay NULL until the first approval; the seen columns
-- hold the latest version seen on connect. A tool is offered only while the two hashes match.
CREATE TABLE mcp_tool_pins (
    server           text NOT NULL,
    tool             text NOT NULL,
    description      text,
    schema_json      jsonb,
    hash             text,
    approved_at      timestamptz,
    seen_description text NOT NULL,
    seen_schema_json jsonb NOT NULL,
    seen_hash        text NOT NULL,
    seen_at          timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server, tool),
    CHECK ((approved_at IS NULL) = (hash IS NULL)),
    CHECK ((hash IS NULL) = (description IS NULL) AND (hash IS NULL) = (schema_json IS NULL))
);
