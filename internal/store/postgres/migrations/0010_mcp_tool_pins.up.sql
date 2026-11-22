-- The description and input schema each MCP tool was first trusted with. Host-wide, not user data:
-- a tool whose live description or schema no longer hashes to its pin is disabled.
CREATE TABLE mcp_tool_pins (
    server      text NOT NULL,
    tool        text NOT NULL,
    description text NOT NULL,
    schema_json jsonb NOT NULL,
    hash        text NOT NULL,
    approved_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (server, tool)
);
