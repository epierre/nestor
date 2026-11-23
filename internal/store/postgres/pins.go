package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/epierre/nestor/internal/store"
)

// RecordToolSeen implements mcp.PinStore. It stores the version of a tool seen on connect and
// returns the approved and seen versions. NewlySeen is set only when the seen version changed, so
// concurrent connects report a new version once.
func (s *Store) RecordToolSeen(ctx context.Context, live store.ToolPin) (store.ToolPinState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return store.ToolPinState{}, fmt.Errorf("record tool seen: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Insert the row if it is missing, then lock it, so two connects cannot both see it as new.
	if _, err := tx.Exec(ctx, `
		INSERT INTO mcp_tool_pins (server, tool, seen_description, seen_schema_json, seen_hash)
		VALUES ($1, $2, '', '{}', '')
		ON CONFLICT (server, tool) DO NOTHING`, live.Server, live.Tool); err != nil {
		return store.ToolPinState{}, fmt.Errorf("record tool seen: %w", err)
	}
	var (
		approved          store.ToolPin
		description, hash *string
		schema            []byte
		approvedAt        *time.Time
		previousSeenHash  string
	)
	err = tx.QueryRow(ctx, `
		SELECT description, schema_json, hash, approved_at, seen_hash FROM mcp_tool_pins
		WHERE server = $1 AND tool = $2 FOR UPDATE`, live.Server, live.Tool).
		Scan(&description, &schema, &hash, &approvedAt, &previousSeenHash)
	if err != nil {
		return store.ToolPinState{}, fmt.Errorf("read tool pin: %w", err)
	}

	state := store.ToolPinState{Seen: live, NewlySeen: previousSeenHash != live.Hash}
	if state.NewlySeen {
		if _, err := tx.Exec(ctx, `
			UPDATE mcp_tool_pins
			SET seen_description = $3, seen_schema_json = $4, seen_hash = $5, seen_at = now()
			WHERE server = $1 AND tool = $2`,
			live.Server, live.Tool, live.Description, live.SchemaJSON, live.Hash); err != nil {
			return store.ToolPinState{}, fmt.Errorf("record tool seen: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return store.ToolPinState{}, fmt.Errorf("record tool seen: %w", err)
	}

	if hash != nil {
		approved = store.ToolPin{
			Server: live.Server, Tool: live.Tool, Description: *description,
			SchemaJSON: schema, Hash: *hash, ApprovedAt: *approvedAt,
		}
		state.Approved = &approved
	}
	return state, nil
}

// ApproveTool approves the version of a tool last seen on connect and returns it. It returns
// store.ErrToolPinNotFound for a tool that has never been seen.
func (s *Store) ApproveTool(ctx context.Context, server, tool string) (store.ToolPin, error) {
	pin := store.ToolPin{Server: server, Tool: tool}
	err := s.pool.QueryRow(ctx, `
		UPDATE mcp_tool_pins
		SET description = seen_description, schema_json = seen_schema_json, hash = seen_hash,
		    approved_at = now()
		WHERE server = $1 AND tool = $2
		RETURNING description, schema_json, hash, approved_at`, server, tool).
		Scan(&pin.Description, &pin.SchemaJSON, &pin.Hash, &pin.ApprovedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ToolPin{}, store.ErrToolPinNotFound
	}
	if err != nil {
		return store.ToolPin{}, fmt.Errorf("approve tool: %w", err)
	}
	return pin, nil
}
