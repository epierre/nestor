package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/store"
)

// fakeApprover approves the one tool it knows.
type fakeApprover struct{ approved []string }

func (f *fakeApprover) ApproveTool(_ context.Context, server, tool string) (store.ToolPin, error) {
	if server != "weather" || tool != "get_forecast" {
		return store.ToolPin{}, store.ErrToolPinNotFound
	}
	f.approved = append(f.approved, server+"/"+tool)
	return store.ToolPin{Server: server, Tool: tool, Description: "Forecast for a city.", Hash: "h", ApprovedAt: time.Now()}, nil
}

func TestMCPApprove(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantOut string
		wantErr string
	}{
		{"approves a seen tool", []string{"approve", "weather", "get_forecast"}, "Forecast for a city.", ""},
		{"unseen tool", []string{"approve", "weather", "nope"}, "", "has not been seen"},
		{"missing tool", []string{"approve", "weather"}, "", "usage"},
		{"unknown subcommand", []string{"list"}, "", "usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := mcpCommand(context.Background(), &fakeApprover{}, tt.args, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("mcpCommand: %v", err)
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(out.String(), "weather__get_forecast") {
				t.Errorf("output %q lacks the approved tool and description", out.String())
			}
		})
	}
}
