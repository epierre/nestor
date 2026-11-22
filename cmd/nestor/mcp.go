package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/store/postgres"
)

const mcpUsage = "usage: nestor mcp approve <server> <tool>"

// toolApprover approves the version of an MCP tool last seen on connect. postgres.Store
// implements it.
type toolApprover interface {
	ApproveTool(ctx context.Context, server, tool string) (store.ToolPin, error)
}

// runMCP handles "nestor mcp ...", opening the database named by the environment.
func runMCP(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	st, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()
	return mcpCommand(ctx, st, args, stdout)
}

// mcpCommand runs one mcp subcommand. Only "approve" exists: it approves the description and
// schema last seen for a tool, which is offered from the next connect on.
func mcpCommand(ctx context.Context, approver toolApprover, args []string, w io.Writer) error {
	if len(args) != 3 || args[0] != "approve" {
		return errors.New(mcpUsage)
	}
	server, tool := args[1], args[2]
	pin, err := approver.ApproveTool(ctx, server, tool)
	if errors.Is(err, store.ErrToolPinNotFound) {
		return fmt.Errorf("tool %s %s has not been seen on connect yet; start nestor with it configured first", server, tool)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "approved %s__%s with description:\n%s\n\nIt is offered from the next connect (restart or reconnect).\n",
		pin.Server, pin.Tool, pin.Description)
	return err
}
