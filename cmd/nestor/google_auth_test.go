package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp"
	"github.com/epierre/nestor/internal/testutil"
)

func TestParseGoogleAuthArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, got googleAuthArgs)
	}{
		{
			name: "default manual mode",
			args: []string{"1"},
			check: func(t *testing.T, got googleAuthArgs) {
				if got.user != 1 || got.key != defaultGoogleOAuthKey || got.listen != "" {
					t.Errorf("got %+v", got)
				}
			},
		},
		{
			name: "flags before user id",
			args: []string{"--key", "custom_key", "--listen", "127.0.0.1:8085", "--scopes", "scope1, scope2", "2"},
			check: func(t *testing.T, got googleAuthArgs) {
				if got.user != 2 || got.key != "custom_key" || got.listen != "127.0.0.1:8085" {
					t.Errorf("got %+v", got)
				}
				if len(got.scopes) != 2 || got.scopes[0] != "scope1" || got.scopes[1] != "scope2" {
					t.Errorf("scopes = %v", got.scopes)
				}
			},
		},
		{
			name: "explicit manual flag",
			args: []string{"3", "--manual"},
			check: func(t *testing.T, got googleAuthArgs) {
				if got.user != 3 || got.listen != "" {
					t.Errorf("got %+v", got)
				}
			},
		},
		{
			name:    "manual and listen together rejected",
			args:    []string{"1", "--manual", "--listen", "127.0.0.1:8085"},
			wantErr: "--manual",
		},
		{
			name:    "non-loopback listen rejected",
			args:    []string{"1", "--listen", "0.0.0.0:8085"},
			wantErr: "loopback",
		},
		{
			name:    "invalid user id rejected",
			args:    []string{"ana"},
			wantErr: "positive integer",
		},
		{
			name:    "zero user id rejected",
			args:    []string{"0"},
			wantErr: "positive integer",
		},
		{
			name:    "missing user id rejected",
			args:    []string{"--manual"},
			wantErr: "usage:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGoogleAuthArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func extractAuthURL(t *testing.T, r io.Reader, copyOut *bytes.Buffer) *url.URL {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		copyOut.WriteString(line)
		copyOut.WriteByte('\n')
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") {
			u, err := url.Parse(trimmed)
			if err != nil {
				t.Fatalf("parse auth url %q: %v", trimmed, err)
			}
			return u
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	t.Fatal("no authorization URL printed")
	return nil
}

func assertNoSecrets(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			t.Errorf("output/error leaked secret %q: %q", s, text)
		}
	}
}

func TestGoogleAuthManualFlow(t *testing.T) {
	const (
		clientID     = "client-id-1"
		clientSecret = "client-secret-sentinel"
		authCode     = "auth-code-secret"
		accessTok    = "access-secret-123"
		refreshTok   = "refresh-secret-123"
	)

	var gotVerifier, gotCode string
	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		gotID, gotSec, ok := r.BasicAuth()
		if !ok {
			gotID = r.FormValue("client_id")
			gotSec = r.FormValue("client_secret")
		}
		if gotID != clientID || gotSec != clientSecret {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}
		gotCode = r.FormValue("code")
		gotVerifier = r.FormValue("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessTok + `","refresh_token":"` + refreshTok + `","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenTS.Close)

	sealer, err := credentials.NewSealer(bytes.Repeat([]byte{5}, credentials.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	st := testutil.NewFakeStore()

	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := googleAuth(context.Background(), st, sealer, googleAuthOptions{
			User:         1,
			Key:          defaultGoogleOAuthKey,
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     tokenTS.URL,
			Scopes:       defaultGoogleScopes,
		}, inR, outW)
		_ = outW.Close()
		errCh <- err
	}()

	var outBuf bytes.Buffer
	authU := extractAuthURL(t, outR, &outBuf)
	q := authU.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", q.Get("code_challenge_method"))
	}
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline", q.Get("access_type"))
	}
	if q.Get("prompt") != "consent" {
		t.Errorf("prompt = %q, want consent", q.Get("prompt"))
	}
	state := q.Get("state")
	if state == "" {
		t.Fatal("missing state parameter")
	}
	challenge := q.Get("code_challenge")
	if challenge == "" {
		t.Fatal("missing code_challenge parameter")
	}

	callback := q.Get("redirect_uri") + "?code=" + url.QueryEscape(authCode) + "&state=" + url.QueryEscape(state) + "\n"
	if _, err := io.WriteString(inW, callback); err != nil {
		t.Fatalf("write callback: %v", err)
	}
	_ = inW.Close()

	rest, _ := io.ReadAll(outR)
	outBuf.Write(rest)
	if err := <-errCh; err != nil {
		t.Fatalf("googleAuth: %v", err)
	}

	if gotCode != authCode {
		t.Errorf("token endpoint code = %q, want %q", gotCode, authCode)
	}
	sum := sha256.Sum256([]byte(gotVerifier))
	if wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:]); wantChallenge != challenge {
		t.Errorf("PKCE challenge mismatch: verifier hashes to %q, auth URL had %q", wantChallenge, challenge)
	}

	cred, err := st.GetCredential(identity.WithUser(context.Background(), 1), 1, defaultGoogleOAuthKey)
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	plain, err := sealer.Open(1, defaultGoogleOAuthKey, cred.Ciphertext, cred.Nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	parsed, err := mcp.ParseOAuthCredential(plain)
	if err != nil {
		t.Fatalf("ParseOAuthCredential: %v", err)
	}
	if parsed.RefreshToken != refreshTok {
		t.Errorf("stored refresh token = %q, want %q", parsed.RefreshToken, refreshTok)
	}

	assertNoSecrets(t, outBuf.String(), clientSecret, authCode, accessTok, refreshTok)
}

func TestGoogleAuthRejectsStateMismatchAndMissingRefreshToken(t *testing.T) {
	const (
		clientID     = "client-id-1"
		clientSecret = "client-secret-sentinel"
		authCode     = "auth-code-secret"
	)

	sealer, err := credentials.NewSealer(bytes.Repeat([]byte{6}, credentials.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}

	t.Run("state mismatch", func(t *testing.T) {
		st := testutil.NewFakeStore()
		outR, outW := io.Pipe()
		inR, inW := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			err := googleAuth(context.Background(), st, sealer, googleAuthOptions{
				User:         1,
				Key:          defaultGoogleOAuthKey,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				TokenURL:     "https://oauth2.googleapis.com/token",
				Scopes:       defaultGoogleScopes,
			}, inR, outW)
			_ = outW.Close()
			errCh <- err
		}()

		var outBuf bytes.Buffer
		authU := extractAuthURL(t, outR, &outBuf)
		callback := authU.Query().Get("redirect_uri") + "?code=" + authCode + "&state=wrong-state\n"
		_, _ = io.WriteString(inW, callback)
		_ = inW.Close()
		rest, _ := io.ReadAll(outR)
		outBuf.Write(rest)

		err := <-errCh
		if err == nil || !strings.Contains(err.Error(), "state") {
			t.Fatalf("err = %v, want state mismatch", err)
		}
		if _, lookupErr := st.GetCredential(identity.WithUser(context.Background(), 1), 1, defaultGoogleOAuthKey); lookupErr == nil {
			t.Fatal("credential stored despite state mismatch")
		}
		assertNoSecrets(t, err.Error()+outBuf.String(), clientSecret, authCode)
	})

	t.Run("missing refresh token", func(t *testing.T) {
		tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"access-secret-123","token_type":"Bearer","expires_in":3600}`))
		}))
		t.Cleanup(tokenTS.Close)

		st := testutil.NewFakeStore()
		outR, outW := io.Pipe()
		inR, inW := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			err := googleAuth(context.Background(), st, sealer, googleAuthOptions{
				User:         1,
				Key:          defaultGoogleOAuthKey,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				TokenURL:     tokenTS.URL,
				Scopes:       defaultGoogleScopes,
			}, inR, outW)
			_ = outW.Close()
			errCh <- err
		}()

		var outBuf bytes.Buffer
		authU := extractAuthURL(t, outR, &outBuf)
		callback := authU.Query().Get("redirect_uri") + "?code=" + authCode + "&state=" + authU.Query().Get("state") + "\n"
		_, _ = io.WriteString(inW, callback)
		_ = inW.Close()
		rest, _ := io.ReadAll(outR)
		outBuf.Write(rest)

		err := <-errCh
		if err == nil || !strings.Contains(err.Error(), "refresh_token") {
			t.Fatalf("err = %v, want missing refresh_token", err)
		}
		if _, lookupErr := st.GetCredential(identity.WithUser(context.Background(), 1), 1, defaultGoogleOAuthKey); lookupErr == nil {
			t.Fatal("credential stored despite missing refresh_token")
		}
		assertNoSecrets(t, err.Error()+outBuf.String(), clientSecret, authCode, "access-secret-123")
	})

	t.Run("token exchange error strips secrets", func(t *testing.T) {
		tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"leaked ` + clientSecret + ` ` + authCode + `"}`))
		}))
		t.Cleanup(tokenTS.Close)

		st := testutil.NewFakeStore()
		outR, outW := io.Pipe()
		inR, inW := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			err := googleAuth(context.Background(), st, sealer, googleAuthOptions{
				User:         1,
				Key:          defaultGoogleOAuthKey,
				ClientID:     clientID,
				ClientSecret: clientSecret,
				TokenURL:     tokenTS.URL,
				Scopes:       defaultGoogleScopes,
			}, inR, outW)
			_ = outW.Close()
			errCh <- err
		}()

		var outBuf bytes.Buffer
		authU := extractAuthURL(t, outR, &outBuf)
		callback := authU.Query().Get("redirect_uri") + "?code=" + authCode + "&state=" + authU.Query().Get("state") + "\n"
		_, _ = io.WriteString(inW, callback)
		_ = inW.Close()
		rest, _ := io.ReadAll(outR)
		outBuf.Write(rest)

		err := <-errCh
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		assertNoSecrets(t, err.Error()+outBuf.String(), clientSecret, authCode)
	})
}

func TestGoogleAuthLoopbackFlow(t *testing.T) {
	const (
		clientID     = "client-id-1"
		clientSecret = "client-secret-sentinel"
		authCode     = "loopback-code-secret"
		accessTok    = "access-secret-loopback"
		refreshTok   = "refresh-secret-loopback"
	)

	tokenTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + accessTok + `","refresh_token":"` + refreshTok + `","token_type":"Bearer","expires_in":3600}`))
	}))
	t.Cleanup(tokenTS.Close)

	sealer, err := credentials.NewSealer(bytes.Repeat([]byte{8}, credentials.KeySize))
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	st := testutil.NewFakeStore()

	outR, outW := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		err := googleAuth(context.Background(), st, sealer, googleAuthOptions{
			User:         2,
			Key:          defaultGoogleOAuthKey,
			ListenAddr:   "127.0.0.1:0",
			ClientID:     clientID,
			ClientSecret: clientSecret,
			TokenURL:     tokenTS.URL,
			Scopes:       defaultGoogleScopes,
		}, strings.NewReader(""), outW)
		_ = outW.Close()
		errCh <- err
	}()

	var outBuf bytes.Buffer
	authU := extractAuthURL(t, outR, &outBuf)
	q := authU.Query()
	cbURL := q.Get("redirect_uri") + "?code=" + url.QueryEscape(authCode) + "&state=" + url.QueryEscape(q.Get("state"))
	resp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}

	rest, _ := io.ReadAll(outR)
	outBuf.Write(rest)
	if err := <-errCh; err != nil {
		t.Fatalf("googleAuth loopback: %v", err)
	}

	cred, err := st.GetCredential(identity.WithUser(context.Background(), 2), 2, defaultGoogleOAuthKey)
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	plain, err := sealer.Open(2, defaultGoogleOAuthKey, cred.Ciphertext, cred.Nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	parsed, err := mcp.ParseOAuthCredential(plain)
	if err != nil {
		t.Fatalf("ParseOAuthCredential: %v", err)
	}
	if parsed.RefreshToken != refreshTok {
		t.Errorf("stored refresh token = %q, want %q", parsed.RefreshToken, refreshTok)
	}
	assertNoSecrets(t, outBuf.String(), clientSecret, authCode, accessTok, refreshTok)
}
