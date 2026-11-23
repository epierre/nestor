// Package mcp connects to MCP servers with the official Go SDK. This file covers the client side
// only: opening a session over a caller-supplied transport and listing the server's tools. The
// transport is chosen by the caller, so stdio and streamable HTTP share one path.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// clientName identifies Nestor to the servers it connects to.
var clientName = &sdk.Implementation{Name: "nestor", Version: "dev"}

// Session is an open connection to one MCP server.
type Session struct {
	cs *sdk.ClientSession
}

// Connect performs the MCP initialise handshake over t and returns the open session.
func Connect(ctx context.Context, t sdk.Transport) (*Session, error) {
	client := sdk.NewClient(clientName, nil)
	cs, err := client.Connect(ctx, t, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp server: %w", err)
	}
	return &Session{cs: cs}, nil
}

// DialHTTP connects to a server over streamable HTTP at url.
func DialHTTP(ctx context.Context, url string) (*Session, error) {
	return Connect(ctx, &sdk.StreamableClientTransport{Endpoint: url, DisableStandaloneSSE: true})
}

// ListTools returns every tool the server offers, following pagination. Nothing is filtered
// here: the allowlist in mcp.yaml decides which tools are exposed.
func (s *Session) ListTools(ctx context.Context) ([]*sdk.Tool, error) {
	var tools []*sdk.Tool
	for tool, err := range s.cs.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list mcp tools: %w", err)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// Ping checks that the server still answers.
func (s *Session) Ping(ctx context.Context) error {
	if err := s.cs.Ping(ctx, nil); err != nil {
		return fmt.Errorf("ping mcp server: %w", err)
	}
	return nil
}

// Close ends the session and, for stdio, the server process.
func (s *Session) Close() error {
	return s.cs.Close()
}

// CallTool invokes a tool on the server with args as its JSON arguments.
func (s *Session) CallTool(ctx context.Context, name string, args json.RawMessage) (*sdk.CallToolResult, error) {
	res, err := s.cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("call mcp tool %q: %w", name, err)
	}
	return res, nil
}
