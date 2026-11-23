package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fixedNow is 16:05 UTC on 22 November 2026: 17:05 in Zurich (CET) and 11:05 in New York (EST).
var fixedNow = time.Date(2026, 11, 22, 16, 5, 0, 0, time.UTC)

func connect(t *testing.T) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := sdk.NewInMemoryTransports()
	if _, err := newServer(func() time.Time { return fixedNow }).Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *sdk.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if text, ok := c.(*sdk.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String(), res.IsError
}

func TestListsReadOnlyTools(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
	}
	slices.Sort(names)
	if want := []string{"convert_time", "get_time"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}

func TestTools(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		args      map[string]any
		want      string // substring of the result
		wantError bool
	}{
		{"time in a zone", "get_time", map[string]any{"timezone": "Europe/Zurich"}, "2026-11-22 17:05 (Europe/Zurich, UTC+01:00)", false},
		{"unknown zone", "get_time", map[string]any{"timezone": "Mars/Olympus"}, "unknown time zone", true},
		{"convert across zones", "convert_time",
			map[string]any{"time": "09:30", "from_timezone": "Europe/Zurich", "to_timezone": "America/New_York"},
			"09:30 in Europe/Zurich is 03:30 in America/New_York", false},
		{"bad time format", "convert_time",
			map[string]any{"time": "9.30", "from_timezone": "Europe/Zurich", "to_timezone": "UTC"},
			"time must be HH:MM", true},
		{"unknown target zone", "convert_time",
			map[string]any{"time": "09:30", "from_timezone": "Europe/Zurich", "to_timezone": "Nowhere/Here"},
			"unknown time zone", true},
	}
	cs := connect(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, isError := call(t, cs, tt.tool, tt.args)
			if isError != tt.wantError {
				t.Errorf("IsError = %v, want %v (result %q)", isError, tt.wantError, got)
			}
			if !strings.Contains(got, tt.want) {
				t.Errorf("result %q does not contain %q", got, tt.want)
			}
		})
	}
}
