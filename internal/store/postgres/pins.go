package postgres

import (
	"context"
	"fmt"

	"github.com/epierre/nestor/internal/store"
)

// PinTool implements mcp.PinStore. It stores pin unless the tool is already pinned and returns the
// stored pin, so the first pin wins even when two connects race.
func (s *Store) PinTool(ctx context.Context, pin store.ToolPin) (store.ToolPin, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO mcp_tool_pins (server, tool, description, schema_json, hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (server, tool) DO NOTHING`,
		pin.Server, pin.Tool, pin.Description, pin.SchemaJSON, pin.Hash)
	if err != nil {
		return store.ToolPin{}, fmt.Errorf("pin tool: %w", err)
	}
	stored := store.ToolPin{Server: pin.Server, Tool: pin.Tool}
	err = s.pool.QueryRow(ctx, `
		SELECT description, schema_json, hash, approved_at FROM mcp_tool_pins
		WHERE server = $1 AND tool = $2`, pin.Server, pin.Tool).
		Scan(&stored.Description, &stored.SchemaJSON, &stored.Hash, &stored.ApprovedAt)
	if err != nil {
		return store.ToolPin{}, fmt.Errorf("read tool pin: %w", err)
	}
	return stored, nil
}
