package config

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

const validFile = `
servers:
  - name: weather
    transport: http
    url: http://mcp-weather:8080/mcp
    image: ghcr.io/example/weather-mcp:1.2.0
    remote: false
    credentials: none
    users: ["*"]
    timeout: 10s
    max_result_bytes: 65536
    tools:
      get_forecast: { risk: read }

  - name: todoist
    transport: http
    url: http://mcp-todoist:8080/mcp
    image: ghcr.io/example/todoist-mcp:1.4.2
    credentials: per_user
    credential_key: todoist_token
    credential_delivery: header
    users: [1]
    tools:
      get_tasks:   { risk: read }
      add_task:    { risk: reversible }
      delete_task: { risk: critical }
`

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    File
		wantErr string // substring; empty means valid
	}{
		{
			name:  "valid file",
			input: validFile,
			want: File{Servers: []Server{
				{
					Name:           "weather",
					Transport:      "http",
					URL:            "http://mcp-weather:8080/mcp",
					Image:          "ghcr.io/example/weather-mcp:1.2.0",
					Credentials:    "none",
					Users:          []string{"*"},
					Timeout:        10 * time.Second,
					MaxResultBytes: 65536,
					Tools:          map[string]Tool{"get_forecast": {Risk: tools.RiskRead}},
				},
				{
					Name:               "todoist",
					Transport:          "http",
					URL:                "http://mcp-todoist:8080/mcp",
					Image:              "ghcr.io/example/todoist-mcp:1.4.2",
					Credentials:        "per_user",
					CredentialKey:      "todoist_token",
					CredentialDelivery: "header",
					Users:              []string{"1"},
					Tools: map[string]Tool{
						"get_tasks":   {Risk: tools.RiskRead},
						"add_task":    {Risk: tools.RiskReversible},
						"delete_task": {Risk: tools.RiskCritical},
					},
				},
			}},
		},
		{
			name:    "misspelled key rejected",
			input:   strings.Replace(validFile, "max_result_bytes", "max_result_byte", 1),
			wantErr: "field max_result_byte not found",
		},
		{
			name:    "tool without risk rejected",
			input:   strings.Replace(validFile, "{ risk: read }", "{ }", 1),
			wantErr: `server "weather" tool "get_forecast" has no risk`,
		},
		{
			name:    "malformed yaml rejected",
			input:   "servers: [",
			wantErr: "yaml",
		},
		{
			name:    "unknown user rejected",
			input:   strings.Replace(validFile, "users: [1]", "users: [9]", 1),
			wantErr: `server "todoist" refers to unknown user "9"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Load(strings.NewReader(tt.input), []identity.UserID{1})
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Load: unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("Load: want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("Load: error %q does not contain %q", err, tt.wantErr)
			}
			if tt.wantErr == "" && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load:\n got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
