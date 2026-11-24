package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp"
	mcpconfig "github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/tools"
)

// mcpDeps is what startMCP needs. Notifier may be nil: alerts are then only logged, as they are
// when ADMIN_USER_ID is unset.
type mcpDeps struct {
	Config      config.Config
	Registry    *tools.Registry
	Pins        mcp.PinStore
	Credentials mcp.CredentialStore
	Notifier    mcp.UserNotifier
	// Users are the active users: mcp.yaml may name only these.
	Users          []identity.UserID
	Getenv         func(string) string
	Dial           func(ctx context.Context, url string) (*mcp.Session, error)
	DialWithHeader func(ctx context.Context, url, headerName, headerValue string) (*mcp.Session, error)
}

// startMCP loads the reviewed mcp.yaml and starts one supervisor per server, which registers the
// server's approved tools while it answers. An invalid file, or a server of a kind that is not
// wired yet, is an error and nothing is started. wait returns once every supervisor has stopped
// after ctx is cancelled.
func startMCP(ctx context.Context, d mcpDeps) (wait func(), err error) {
	noop := func() {}
	if d.Config.MCPConfigPath == "" {
		return noop, nil
	}
	f, err := os.Open(d.Config.MCPConfigPath)
	if err != nil {
		return noop, fmt.Errorf("open mcp config: %w", err)
	}
	defer f.Close()
	file, err := mcpconfig.Load(f, d.Users)
	if err != nil {
		return noop, err
	}
	if err := supported(file); err != nil {
		return noop, err
	}

	getenv := d.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	var sealer *credentials.Sealer
	for _, s := range file.Servers {
		needsSealer := s.CredentialDelivery == "header"
		if s.CredentialDelivery == "oauth2" {
			_, _, missing := oauthClientCreds(s, getenv)
			needsSealer = len(missing) == 0
		}
		if needsSealer && sealer == nil {
			if d.Config.CredentialsKey == nil {
				return noop, fmt.Errorf("server %q needs per-user credentials, but CREDENTIALS_KEY is not set", s.Name)
			}
			if sealer, err = credentials.NewSealer(d.Config.CredentialsKey); err != nil {
				return noop, err
			}
		}
	}

	registrar := &mcp.Registrar{Registry: d.Registry, Pins: d.Pins}
	if d.Notifier != nil && d.Config.AdminUserID != 0 {
		registrar.Alerts = mcp.AdminAlerts{Notifier: d.Notifier, Admin: d.Config.AdminUserID}
	}

	dial := d.Dial
	if dial == nil {
		dial = mcp.DialHTTP
	}
	dialWithHeader := d.DialWithHeader
	if dialWithHeader == nil {
		dialWithHeader = mcp.DialHTTPWithHeader
	}

	var wg sync.WaitGroup
	for _, server := range file.Servers {
		sup := &mcp.Supervisor{Registrar: registrar, Server: server, Interval: server.HealthInterval}
		switch {
		case server.CredentialDelivery == "header":
			users := mcp.NewUserConnector(server, d.Credentials, sealer)
			sup.Users = users
			sup.Dial = listAsFirstUser(server, d.Users, users)
		case server.CredentialDelivery == "oauth2":
			id, secret, missing := oauthClientCreds(server, getenv)
			if len(missing) > 0 {
				slog.WarnContext(ctx, "mcp oauth2 client env var not set; server disabled",
					"server", server.Name, "missing_env", strings.Join(missing, ","))
				continue
			}
			users := mcp.NewOAuthUserConnector(server, d.Credentials, sealer, mcp.OAuthConfig{
				ClientID:     id,
				ClientSecret: secret,
				TokenURL:     server.OAuthTokenURL,
			})
			sup.Users = users
			sup.Dial = listAsFirstUser(server, d.Users, users)
		case server.Credentials == "shared":
			secret := strings.TrimSpace(getenv(server.CredentialEnv))
			if secret == "" {
				slog.WarnContext(ctx, "mcp shared credential env var not set; server disabled",
					"server", server.Name, "credential_env", server.CredentialEnv)
				continue
			}
			headerName, headerValue := sharedAuthHeader(server.CredentialHeader, secret)
			url := server.URL
			sup.Dial = func(ctx context.Context) (*mcp.Session, error) {
				return dialWithHeader(ctx, url, headerName, headerValue)
			}
		default:
			url := server.URL
			sup.Dial = func(ctx context.Context) (*mcp.Session, error) { return dial(ctx, url) }
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sup.Run(ctx)
		}()
	}
	return wg.Wait, nil
}

func sharedAuthHeader(customHeader, secret string) (string, string) {
	if customHeader == "" {
		return "Authorization", "Bearer " + secret
	}
	return customHeader, secret
}

func oauthClientCreds(s mcpconfig.Server, getenv func(string) string) (id, secret string, missing []string) {
	id = strings.TrimSpace(getenv(s.OAuthClientIDEnv))
	secret = strings.TrimSpace(getenv(s.OAuthClientSecretEnv))
	if id == "" {
		missing = append(missing, s.OAuthClientIDEnv)
	}
	if secret == "" {
		missing = append(missing, s.OAuthClientSecretEnv)
	}
	return id, secret, missing
}

// supported rejects the kinds of server that startup does not wire yet, so a configured server is
// never silently skipped.
func supported(file mcpconfig.File) error {
	for _, s := range file.Servers {
		switch {
		case s.Transport != "http":
			return fmt.Errorf("server %q: %s transport is not supported yet", s.Name, s.Transport)
		case s.CredentialDelivery == "container":
			return fmt.Errorf("server %q: container credential delivery is not supported yet (task 3.4)", s.Name)
		}
	}
	return nil
}

// listAsFirstUser dials a header-delivery server as the first active user it is enabled for who
// has a stored credential: the tool list and health check need some user's token.
func listAsFirstUser(server mcpconfig.Server, active []identity.UserID, users *mcp.UserConnector) func(context.Context) (*mcp.Session, error) {
	return func(ctx context.Context) (*mcp.Session, error) {
		for _, u := range active {
			if !mcpconfig.UsersInclude(server.Users, u) {
				continue
			}
			userCtx := identity.WithUser(ctx, u)
			if users.HasCredential(userCtx, u) {
				return users.ForUser(userCtx, u)
			}
		}
		return nil, errors.New("no enabled user has a stored credential for this server")
	}
}
