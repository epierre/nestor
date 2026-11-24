package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/store"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrMissingCredential is returned when a user has no stored credential for a per_user server.
var ErrMissingCredential = errors.New("missing credential")

// CredentialStore reads a user's encrypted credentials. store.Store implements it.
type CredentialStore interface {
	GetCredential(ctx context.Context, user identity.UserID, key string) (store.Credential, error)
}

// UserConnector holds one session per user to a per_user server with header or oauth2 delivery.
// Each session sends only its own user's token, so no request carries another user's credential.
type UserConnector struct {
	server config.Server
	creds  CredentialStore
	sealer *credentials.Sealer
	oauth  *OAuthConfig

	mu       sync.Mutex
	sessions map[identity.UserID]userSession
}

type userSession struct {
	session *Session
	nonce   string // nonce of the credential the session was opened with
}

// NewUserConnector returns a connector for a static-header per_user server, reading tokens from creds.
func NewUserConnector(server config.Server, creds CredentialStore, sealer *credentials.Sealer) *UserConnector {
	return &UserConnector{
		server:   server,
		creds:    creds,
		sealer:   sealer,
		sessions: make(map[identity.UserID]userSession),
	}
}

// NewOAuthUserConnector returns a connector for an oauth2 per_user server, decrypting each user's
// refresh token from creds and refreshing short-lived access tokens transparently.
func NewOAuthUserConnector(server config.Server, creds CredentialStore, sealer *credentials.Sealer, oauth OAuthConfig) *UserConnector {
	cp := oauth
	return &UserConnector{
		server:   server,
		creds:    creds,
		sealer:   sealer,
		oauth:    &cp,
		sessions: make(map[identity.UserID]userSession),
	}
}

// ForUser returns the session for user, opening it with the user's current token. A session is
// reused until the stored credential changes. ctx must carry user.
func (c *UserConnector) ForUser(ctx context.Context, user identity.UserID) (*Session, error) {
	cred, err := c.credential(ctx, user)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.sessions[user]; ok {
		if cached.nonce == string(cred.Nonce) {
			return cached.session, nil
		}
		_ = cached.session.Close()
		delete(c.sessions, user)
	}

	token, err := c.sealer.Open(user, c.server.CredentialKey, cred.Ciphertext, cred.Nonce)
	if err != nil {
		return nil, fmt.Errorf("credential %s for user %d: %w", c.server.CredentialKey, user, err)
	}
	var httpClient *http.Client
	if c.oauth != nil {
		oauthCred, err := ParseOAuthCredential(token)
		if err != nil {
			return nil, fmt.Errorf("credential %s for user %d: %w", c.server.CredentialKey, user, err)
		}
		httpClient = oauthHTTPClient(c.server.Name, *c.oauth, oauthCred.RefreshToken)
	} else {
		httpClient = &http.Client{Transport: bearer{token: string(token), base: http.DefaultTransport}}
	}
	transport := &sdk.StreamableClientTransport{
		Endpoint:             c.server.URL,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}
	s, err := Connect(ctx, transport)
	if err != nil {
		return nil, err
	}
	c.sessions[user] = userSession{session: s, nonce: string(cred.Nonce)}
	return s, nil
}

// HasCredential reports whether user has a stored credential for the server. A missing one is
// logged by key name, never by value. Any other failure also hides the tools.
func (c *UserConnector) HasCredential(ctx context.Context, user identity.UserID) bool {
	_, err := c.credential(ctx, user)
	return err == nil
}

// Close ends every user's session.
func (c *UserConnector) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for user, s := range c.sessions {
		_ = s.session.Close()
		delete(c.sessions, user)
	}
}

func (c *UserConnector) sessionFor(ctx context.Context) (*Session, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return nil, err
	}
	return c.ForUser(ctx, user)
}

func (c *UserConnector) credential(ctx context.Context, user identity.UserID) (store.Credential, error) {
	cred, err := c.creds.GetCredential(ctx, user, c.server.CredentialKey)
	if errors.Is(err, store.ErrCredentialNotFound) {
		slog.WarnContext(ctx, "missing credential for mcp server",
			"server", c.server.Name, "user_id", user, "credential_key", c.server.CredentialKey)
		return store.Credential{}, fmt.Errorf("server %s: %w %s", c.server.Name, ErrMissingCredential, c.server.CredentialKey)
	}
	if err != nil {
		slog.ErrorContext(ctx, "mcp credential lookup failed",
			"server", c.server.Name, "user_id", user, "error", err)
		return store.Credential{}, fmt.Errorf("server %s: read credential: %w", c.server.Name, err)
	}
	return cred, nil
}

// bearer adds the user's token to every request of one session.
type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}
