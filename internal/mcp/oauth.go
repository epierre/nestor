package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

const defaultOAuthHTTPTimeout = 10 * time.Second

// OAuthConfig holds the host-wide OAuth 2.0 client settings for one oauth2-delivered MCP server.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	TokenURL     string
	HTTPClient   *http.Client // optional; defaults to a 10s-timeout client
}

// OAuthCredential is the JSON payload stored encrypted per user in user_credentials.
type OAuthCredential struct {
	RefreshToken string `json:"refresh_token"`
}

// MarshalOAuthCredential encodes a non-empty refresh token as an OAuthCredential JSON payload.
func MarshalOAuthCredential(refreshToken string) ([]byte, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return nil, errors.New("oauth refresh token must not be empty")
	}
	return json.Marshal(OAuthCredential{RefreshToken: refreshToken})
}

// ParseOAuthCredential decodes an encrypted OAuthCredential JSON payload without echoing content
// on error.
func ParseOAuthCredential(raw []byte) (OAuthCredential, error) {
	var cred OAuthCredential
	if err := json.Unmarshal(raw, &cred); err != nil {
		return OAuthCredential{}, errors.New("invalid oauth credential JSON")
	}
	if strings.TrimSpace(cred.RefreshToken) == "" {
		return OAuthCredential{}, errors.New("oauth credential missing refresh_token")
	}
	return cred, nil
}

func oauthHTTPClient(server string, cfg OAuthConfig, refreshToken string) *http.Client {
	refreshClient := cfg.HTTPClient
	if refreshClient == nil {
		refreshClient = &http.Client{Timeout: defaultOAuthHTTPTimeout}
	}
	// Bind a background context with a bounded HTTP client so cancelling a single MCP tool call
	// does not poison future token refreshes on the shared ReuseTokenSource.
	tokenCtx := context.WithValue(context.Background(), oauth2.HTTPClient, refreshClient)
	oc := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     oauth2.Endpoint{TokenURL: cfg.TokenURL},
	}
	src := oauth2.ReuseTokenSource(nil, oc.TokenSource(tokenCtx, &oauth2.Token{RefreshToken: refreshToken}))
	return &http.Client{
		Transport: tokenSourceTransport{server: server, source: src, base: http.DefaultTransport},
	}
}

type tokenSourceTransport struct {
	server string
	source oauth2.TokenSource
	base   http.RoundTripper
}

func (t tokenSourceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tok, err := t.source.Token()
	if err != nil {
		return nil, sanitizeTokenRefreshErr(t.server, err)
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	return t.base.RoundTrip(r)
}

// sanitizeTokenRefreshErr strips token values, client secrets, and raw response bodies from OAuth
// token refresh errors before they can reach logs or tool errors.
func sanitizeTokenRefreshErr(server string, err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil {
		if re.ErrorCode != "" {
			return fmt.Errorf("server %s: oauth2 token refresh failed (status %d, code %s)",
				server, re.Response.StatusCode, re.ErrorCode)
		}
		return fmt.Errorf("server %s: oauth2 token refresh failed (status %d)",
			server, re.Response.StatusCode)
	}
	return fmt.Errorf("server %s: oauth2 token refresh failed", server)
}
