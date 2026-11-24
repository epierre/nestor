package config

import (
	"strings"
	"testing"
	"time"

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
				s.CredentialDelivery = "header"
			},
		},
		{
			name: "per_user without credential delivery",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
			},
			wantErr: `server "weather" uses per_user credentials but sets no credential_delivery`,
		},
		{
			name: "unknown credential delivery",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "query"
			},
			wantErr: `server "weather" has unknown credential_delivery "query"`,
		},
		{
			name: "header delivery refused for remote servers",
			mutate: func(s *Server) {
				s.URL = "https://mcp.example.com/mcp"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "header"
			},
			wantErr: `server "weather" sends tokens in a header, which needs remote: false`,
		},
		{
			name: "header delivery with per-user URL",
			mutate: func(s *Server) {
				s.URL = "http://mcp-todoist-{user}:8080/mcp"
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "header"
			},
			wantErr: `server "weather" uses header delivery, so its URL must not contain {user}`,
		},
		{
			name: "header delivery over stdio",
			mutate: func(s *Server) {
				s.Transport = "stdio"
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "header"
			},
			wantErr: `server "weather" uses header delivery, which needs transport http`,
		},
		{
			name: "container delivery with per-user URL",
			mutate: func(s *Server) {
				s.URL = "http://mcp-notes-{user}:8080/mcp"
				s.Credentials = "per_user"
				s.CredentialKey = "notes_token"
				s.CredentialDelivery = "container"
			},
		},
		{
			name: "container delivery without per-user URL",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "notes_token"
				s.CredentialDelivery = "container"
			},
			wantErr: `server "weather" uses container delivery, so its URL must contain {user}`,
		},
		{
			name:    "credential delivery without per_user credentials",
			mutate:  func(s *Server) { s.CredentialDelivery = "header" },
			wantErr: `server "weather" sets credential_delivery but does not use per_user credentials`,
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
		{
			name: "shared valid default bearer",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
			},
		},
		{
			name: "shared valid custom header remote https",
			mutate: func(s *Server) {
				s.URL = "https://mapstools.googleapis.com/mcp"
				s.Remote = true
				s.Credentials = "shared"
				s.CredentialEnv = "GOOGLE_MAPS_API_KEY"
				s.CredentialHeader = "X-Goog-Api-Key"
			},
		},
		{
			name: "shared without credential_env",
			mutate: func(s *Server) {
				s.Credentials = "shared"
			},
			wantErr: `names no credential_env`,
		},
		{
			name: "shared with invalid credential_env hyphen",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "bad-env"
			},
			wantErr: `invalid credential_env`,
		},
		{
			name: "shared with invalid credential_env leading digit",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "1KEY"
			},
			wantErr: `invalid credential_env`,
		},
		{
			name: "shared with invalid credential_env lowercase",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "lower"
			},
			wantErr: `invalid credential_env`,
		},
		{
			name: "shared over stdio",
			mutate: func(s *Server) {
				s.Transport = "stdio"
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
			},
			wantErr: `needs transport http`,
		},
		{
			name: "shared with credential_key",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
				s.CredentialKey = "some_key"
			},
			wantErr: `sets credential_key`,
		},
		{
			name: "shared with credential_delivery",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
				s.CredentialDelivery = "header"
			},
			wantErr: `sets credential_delivery`,
		},
		{
			name: "shared with invalid credential_header space",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
				s.CredentialHeader = "Bad Header"
			},
			wantErr: `invalid credential_header`,
		},
		{
			name: "shared with invalid credential_header colon",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
				s.CredentialHeader = "X-Bad:Header"
			},
			wantErr: `invalid credential_header`,
		},
		{
			name: "shared with invalid credential_header newline",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
				s.CredentialHeader = "X-Bad\nHeader"
			},
			wantErr: `invalid credential_header`,
		},
		{
			name: "shared remote with plain http URL",
			mutate: func(s *Server) {
				s.URL = "http://maps.example.com/mcp"
				s.Remote = true
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
			},
			wantErr: `needs an https URL`,
		},
		{
			name: "credential_env set on none",
			mutate: func(s *Server) {
				s.Credentials = "none"
				s.CredentialEnv = "EXAMPLE_TOKEN"
			},
			wantErr: `does not use shared credentials`,
		},
		{
			name: "credential_header set on none",
			mutate: func(s *Server) {
				s.Credentials = "none"
				s.CredentialHeader = "X-Goog-Api-Key"
			},
			wantErr: `does not use shared credentials`,
		},
		{
			name: "credential_env set on per_user",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "header"
				s.CredentialEnv = "EXAMPLE_TOKEN"
			},
			wantErr: `does not use shared credentials`,
		},
		{
			name: "credential_header set on per_user",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "header"
				s.CredentialHeader = "X-Goog-Api-Key"
			},
			wantErr: `does not use shared credentials`,
		},
		{
			name:   "positive health_interval",
			mutate: func(s *Server) { s.HealthInterval = 5 * time.Minute },
		},
		{
			name:    "negative health_interval",
			mutate:  func(s *Server) { s.HealthInterval = -1 * time.Second },
			wantErr: `negative health_interval`,
		},
		{
			name: "oauth2 valid remote server",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
		},
		{
			name: "oauth2 valid local server",
			mutate: func(s *Server) {
				s.URL = "http://127.0.0.1:8080/mcp"
				s.Remote = false
				s.Credentials = "per_user"
				s.CredentialKey = "local_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "http://127.0.0.1:8081/token"
				s.OAuthClientIDEnv = "LOCAL_OAUTH_ID"
				s.OAuthClientSecretEnv = "LOCAL_OAUTH_SECRET"
			},
		},
		{
			name: "oauth2 over stdio",
			mutate: func(s *Server) {
				s.Transport = "stdio"
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `needs transport http`,
		},
		{
			name: "oauth2 with per-user URL placeholder",
			mutate: func(s *Server) {
				s.URL = "http://mcp-oauth-{user}:8080/mcp"
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `must not contain {user}`,
		},
		{
			name: "oauth2 missing oauth_token_url",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `no oauth_token_url`,
		},
		{
			name: "oauth2 invalid oauth_token_url",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "not-a-url"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `invalid oauth_token_url`,
		},
		{
			name: "oauth2 remote with plain http url",
			mutate: func(s *Server) {
				s.URL = "http://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `needs an https URL`,
		},
		{
			name: "oauth2 remote with plain http oauth_token_url",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "http://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `oauth_token_url needs an https URL`,
		},
		{
			name: "oauth2 missing oauth_client_id_env",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `no oauth_client_id_env`,
		},
		{
			name: "oauth2 invalid oauth_client_id_env",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "bad-id"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `invalid oauth_client_id_env`,
		},
		{
			name: "oauth2 missing oauth_client_secret_env",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
			},
			wantErr: `no oauth_client_secret_env`,
		},
		{
			name: "oauth2 invalid oauth_client_secret_env",
			mutate: func(s *Server) {
				s.URL = "https://calendarmcp.googleapis.com/mcp/v1"
				s.Remote = true
				s.Credentials = "per_user"
				s.CredentialKey = "google_oauth"
				s.CredentialDelivery = "oauth2"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
				s.OAuthClientSecretEnv = "1SECRET"
			},
			wantErr: `invalid oauth_client_secret_env`,
		},
		{
			name: "oauth_token_url set on none",
			mutate: func(s *Server) {
				s.Credentials = "none"
				s.OAuthTokenURL = "https://oauth2.googleapis.com/token"
			},
			wantErr: `does not use oauth2 credential delivery`,
		},
		{
			name: "oauth_client_id_env set on shared",
			mutate: func(s *Server) {
				s.Credentials = "shared"
				s.CredentialEnv = "EXAMPLE_TOKEN"
				s.OAuthClientIDEnv = "GOOGLE_OAUTH_CLIENT_ID"
			},
			wantErr: `does not use oauth2 credential delivery`,
		},
		{
			name: "oauth_client_secret_env set on header",
			mutate: func(s *Server) {
				s.Credentials = "per_user"
				s.CredentialKey = "todoist_token"
				s.CredentialDelivery = "header"
				s.OAuthClientSecretEnv = "GOOGLE_OAUTH_CLIENT_SECRET"
			},
			wantErr: `does not use oauth2 credential delivery`,
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
