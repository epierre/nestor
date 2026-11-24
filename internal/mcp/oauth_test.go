package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp/config"
	"github.com/epierre/nestor/internal/testutil"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestUserConnectorOAuthTwoUsersAndRefresh(t *testing.T) {
	const (
		clientID     = "oauth-client-id"
		clientSecret = "client-secret-sentinel"
	)

	var tokenMu sync.Mutex
	u1Calls := 0
	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "refresh_token" {
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		gotID, gotSecret, ok := r.BasicAuth()
		if !ok {
			gotID = r.FormValue("client_id")
			gotSecret = r.FormValue("client_secret")
		}
		if gotID != clientID || gotSecret != clientSecret {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		tokenMu.Lock()
		defer tokenMu.Unlock()
		switch r.FormValue("refresh_token") {
		case "refresh-user-1":
			u1Calls++
			switch u1Calls {
			case 1:
				_, _ = w.Write([]byte(`{"access_token":"access-u1-1","token_type":"Bearer","expires_in":1}`))
			case 2:
				_, _ = w.Write([]byte(`{"access_token":"access-u1-2","token_type":"Bearer","expires_in":1}`))
			default:
				_, _ = w.Write([]byte(`{"access_token":"access-u1-3","token_type":"Bearer","expires_in":3600}`))
			}
		case "refresh-user-2":
			_, _ = w.Write([]byte(`{"access_token":"access-u2-1","token_type":"Bearer","expires_in":3600}`))
		case "refresh-user-1-rotated":
			_, _ = w.Write([]byte(`{"access_token":"access-u1-rotated-1","token_type":"Bearer","expires_in":3600}`))
		case "refresh-broken-secret":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"echo refresh-broken-secret client-secret-sentinel"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		}
	}))
	t.Cleanup(tokenTS.Close)

	srv := sdk.NewServer(&sdk.Implementation{Name: "workspace", Version: "0"}, nil)
	srv.AddTool(&sdk.Tool{Name: "whoami", Description: "Who am I.", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			auth := ""
			if req.Extra != nil && req.Extra.Header != nil {
				auth = req.Extra.Header.Get("Authorization")
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: auth}}}, nil
		})
	mcpTS := httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil))
	t.Cleanup(mcpTS.Close)

	key := bytes.Repeat([]byte{9}, credentials.KeySize)
	sealer, err := credentials.NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	st := testutil.NewFakeStore()
	ctx := context.Background()

	storeRefresh := func(user identity.UserID, refresh string) {
		t.Helper()
		payload, err := MarshalOAuthCredential(refresh)
		if err != nil {
			t.Fatalf("MarshalOAuthCredential: %v", err)
		}
		ct, nonce, err := sealer.Seal(user, "google_oauth", payload)
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
		if err := st.SetCredential(identity.WithUser(ctx, user), user, "google_oauth", ct, nonce); err != nil {
			t.Fatalf("SetCredential: %v", err)
		}
	}

	storeRefresh(1, "refresh-user-1")
	storeRefresh(2, "refresh-user-2")
	storeRefresh(3, "refresh-broken-secret")

	conn := NewOAuthUserConnector(config.Server{
		Name:                 "google-calendar",
		Transport:            "http",
		URL:                  mcpTS.URL + "/mcp",
		Credentials:          "per_user",
		CredentialKey:        "google_oauth",
		CredentialDelivery:   "oauth2",
		OAuthTokenURL:        tokenTS.URL,
		OAuthClientIDEnv:     "GOOGLE_OAUTH_CLIENT_ID",
		OAuthClientSecretEnv: "GOOGLE_OAUTH_CLIENT_SECRET",
	}, st, sealer, OAuthConfig{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURL:     tokenTS.URL,
	})
	t.Cleanup(conn.Close)

	callWhoami := func(user identity.UserID) (string, error) {
		s, err := conn.ForUser(identity.WithUser(ctx, user), user)
		if err != nil {
			return "", err
		}
		res, err := s.CallTool(identity.WithUser(ctx, user), "whoami", json.RawMessage(`{}`))
		if err != nil {
			return "", err
		}
		return resultText(res), nil
	}

	// User 1's Connect sends initialize (access-u1-1, expires_in=1) and notifications/initialized
	// (refreshes to access-u1-2, expires_in=1), then CallTool refreshes to access-u1-3 (expires_in=3600).
	got, err := callWhoami(1)
	if err != nil {
		t.Fatalf("user 1 call 1: %v", err)
	}
	if got != "Bearer access-u1-3" {
		t.Errorf("user 1 call 1 = %q, want Bearer access-u1-3", got)
	}

	// User 2 gets only user 2's token.
	got, err = callWhoami(2)
	if err != nil {
		t.Fatalf("user 2 call 1: %v", err)
	}
	if got != "Bearer access-u2-1" {
		t.Errorf("user 2 call 1 = %q, want Bearer access-u2-1", got)
	}

	// User 1's second call reuses the non-expired access-u1-3 without another refresh.
	got, err = callWhoami(1)
	if err != nil {
		t.Fatalf("user 1 call 2: %v", err)
	}
	if got != "Bearer access-u1-3" {
		t.Errorf("user 1 call 2 = %q, want Bearer access-u1-3", got)
	}
	tokenMu.Lock()
	gotU1Calls := u1Calls
	tokenMu.Unlock()
	if gotU1Calls != 3 {
		t.Errorf("user 1 token refresh calls = %d, want 3 (no extra refresh while valid)", gotU1Calls)
	}

	// Rotating user 1's credential (new nonce) replaces the session cleanly.
	storeRefresh(1, "refresh-user-1-rotated")
	got, err = callWhoami(1)
	if err != nil {
		t.Fatalf("user 1 rotated call: %v", err)
	}
	if got != "Bearer access-u1-rotated-1" {
		t.Errorf("user 1 rotated = %q, want Bearer access-u1-rotated-1", got)
	}

	// Broken refresh token returns an error without leaking refresh token or client secret.
	_, err = callWhoami(3)
	if err == nil {
		t.Fatal("user 3 expected refresh error, got nil")
	}
	if strings.Contains(err.Error(), "refresh-broken-secret") || strings.Contains(err.Error(), clientSecret) {
		t.Fatalf("refresh error leaked secret: %v", err)
	}
}

func TestParseOAuthCredentialValidation(t *testing.T) {
	if _, err := MarshalOAuthCredential("   "); err == nil {
		t.Error("MarshalOAuthCredential accepted whitespace")
	}
	for _, bad := range []string{`not-json-secret-sentinel`, `{}`, `{"refresh_token":"   "}`} {
		_, err := ParseOAuthCredential([]byte(bad))
		if err == nil {
			t.Errorf("ParseOAuthCredential(%q) = nil error, want error", bad)
			continue
		}
		if strings.Contains(err.Error(), "secret-sentinel") {
			t.Errorf("ParseOAuthCredential echoed input: %v", err)
		}
	}
}
