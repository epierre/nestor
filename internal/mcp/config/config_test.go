package config

import (
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

var knownUsers = []identity.UserID{1, 2}

func validServer() Server {
	return Server{
		Name:        "weather",
		Transport:   "http",
		URL:         "http://mcp-weather:8080/mcp",
		Image:       "ghcr.io/example/weather-mcp:1.2.0",
		Credentials: "none",
		Users:       []string{"*"},
		Tools:       map[string]Tool{"get_forecast": {Risk: tools.RiskRead}},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(s *Server)
		servers []Server
		users   []identity.UserID
		wantErr string // substring; empty means valid
	}{
		{name: "valid server", mutate: func(*Server) {}},
		{
			name:    "tool without risk",
			mutate:  func(s *Server) { s.Tools["add_task"] = Tool{} },
			wantErr: `server "weather" tool "add_task" has no risk`,
		},
		{
			name:    "unknown risk",
			mutate:  func(s *Server) { s.Tools["get_forecast"] = Tool{Risk: "dangerous"} },
			wantErr: `server "weather" tool "get_forecast" has unknown risk "dangerous"`,
		},
		{
			name:    "duplicate server names",
			servers: []Server{validServer(), validServer()},
			wantErr: `server name "weather" is not unique`,
		},
		{
			name:    "unknown transport",
			mutate:  func(s *Server) { s.Transport = "websocket" },
			wantErr: `server "weather" has unknown transport "websocket"`,
		},
		{
			name:    "unknown credentials mode",
			mutate:  func(s *Server) { s.Credentials = "shared-ish" },
			wantErr: `server "weather" has unknown credentials "shared-ish"`,
		},
		{
			name: "per_user without credential key",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
			},
			wantErr: `server "weather" uses per_user credentials but names no credential_key`,
		},
		{
			name: "per_user with credential key",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
			},
		},
		{
			name:    "user refers to unknown user",
			mutate:  func(s *Server) { s.Users = []string{"3"} },
			wantErr: `server "weather" refers to unknown user "3"`,
		},
		{
			name:    "user list not a number or wildcard",
			mutate:  func(s *Server) { s.Users = []string{"ana"} },
			wantErr: `server "weather" refers to unknown user "ana"`,
		},
		{
			name:    "known user ids",
			mutate:  func(s *Server) { s.Users = []string{"1", "2"} },
			wantErr: "",
		},
		{
			name:    "public host without remote flag",
			mutate:  func(s *Server) { s.URL = "https://mcp.example.com/mcp" },
			wantErr: `server "weather" points to public host "mcp.example.com" but is not marked remote`,
		},
		{
			name:   "public host marked remote",
			mutate: func(s *Server) { s.URL = "https://mcp.example.com/mcp"; s.Remote = true },
		},
		{
			name:   "loopback host",
			mutate: func(s *Server) { s.URL = "http://127.0.0.1:8080/mcp" },
		},
		{
			name:   "private address",
			mutate: func(s *Server) { s.URL = "http://10.0.0.5:8080/mcp" },
		},
		{
			name:   "per-user placeholder in compose host",
			mutate: func(s *Server) { s.URL = "http://mcp-todoist-{user}:8080/mcp" },
		},
		{
			name:    "per-user placeholder with public host",
			mutate:  func(s *Server) { s.URL = "https://{user}.example.com/mcp" },
			wantErr: `points to public host`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			servers := tt.servers
			if servers == nil {
				s := validServer()
				tt.mutate(&s)
				servers = []Server{s}
			}
			users := tt.users
			if users == nil {
				users = knownUsers
			}

			err := Validate(File{Servers: servers}, users)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate: unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Validate: want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("Validate: error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
