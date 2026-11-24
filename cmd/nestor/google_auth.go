package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/credentials"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/mcp"
	"github.com/epierre/nestor/internal/store"
	"github.com/epierre/nestor/internal/store/postgres"
	"golang.org/x/oauth2"
)

const (
	googleAuthURL            = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL           = "https://oauth2.googleapis.com/token"
	defaultGoogleOAuthKey    = "google_oauth"
	defaultGoogleRedirectURI = "http://127.0.0.1:8085/callback"
	googleAuthUsage          = "usage: nestor credentials google-auth <user-id> [--key google_oauth] [--manual | --listen 127.0.0.1:8085] [--scopes ...]"
	googleExchangeTimeout    = 15 * time.Second
)

var defaultGoogleScopes = []string{
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/contacts.readonly",
	"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
	"https://www.googleapis.com/auth/calendar.events.freebusy",
	"https://www.googleapis.com/auth/calendar.events",
	"https://www.googleapis.com/auth/documents",
	"https://www.googleapis.com/auth/drive.readonly",
	"https://www.googleapis.com/auth/drive.file",
}

type googleAuthArgs struct {
	user   identity.UserID
	key    string
	listen string
	scopes []string
}

type googleAuthOptions struct {
	User         identity.UserID
	Key          string
	ListenAddr   string
	ClientID     string
	ClientSecret string
	AuthURL      string
	TokenURL     string
	Scopes       []string
}

func runGoogleAuth(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout io.Writer) error {
	parsed, err := parseGoogleAuthArgs(args)
	if err != nil {
		return err
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.CredentialsKey == nil {
		return errors.New("CREDENTIALS_KEY is not set")
	}
	clientID := strings.TrimSpace(getenv("GOOGLE_OAUTH_CLIENT_ID"))
	if clientID == "" {
		return errors.New("GOOGLE_OAUTH_CLIENT_ID is not set")
	}
	clientSecret := strings.TrimSpace(getenv("GOOGLE_OAUTH_CLIENT_SECRET"))
	if clientSecret == "" {
		return errors.New("GOOGLE_OAUTH_CLIENT_SECRET is not set")
	}

	sealer, err := credentials.NewSealer(cfg.CredentialsKey)
	if err != nil {
		return err
	}
	st, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	return googleAuth(ctx, st, sealer, googleAuthOptions{
		User:         parsed.user,
		Key:          parsed.key,
		ListenAddr:   parsed.listen,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AuthURL:      googleAuthURL,
		TokenURL:     googleTokenURL,
		Scopes:       parsed.scopes,
	}, stdin, stdout)
}

func parseGoogleAuthArgs(args []string) (googleAuthArgs, error) {
	out := googleAuthArgs{
		key:    defaultGoogleOAuthKey,
		scopes: append([]string(nil), defaultGoogleScopes...),
	}
	var manual bool
	var userRaw string

	readValue := func(i *int, flag, inline string, hasInline bool) (string, error) {
		if hasInline {
			return inline, nil
		}
		if *i+1 >= len(args) {
			return "", fmt.Errorf("flag %s requires a value", flag)
		}
		*i++
		return args[*i], nil
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			if userRaw != "" {
				return googleAuthArgs{}, errors.New(googleAuthUsage)
			}
			userRaw = arg
			continue
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		switch name {
		case "--manual":
			if hasInline {
				return googleAuthArgs{}, errors.New(googleAuthUsage)
			}
			manual = true
		case "--key":
			v, err := readValue(&i, name, inline, hasInline)
			if err != nil {
				return googleAuthArgs{}, err
			}
			if strings.TrimSpace(v) == "" {
				return googleAuthArgs{}, errors.New("credential key must not be empty")
			}
			out.key = strings.TrimSpace(v)
		case "--listen":
			v, err := readValue(&i, name, inline, hasInline)
			if err != nil {
				return googleAuthArgs{}, err
			}
			if !isLoopbackListenAddr(v) {
				return googleAuthArgs{}, fmt.Errorf("--listen address %q must use a loopback host (127.0.0.1, ::1, or localhost)", v)
			}
			out.listen = v
		case "--scopes":
			v, err := readValue(&i, name, inline, hasInline)
			if err != nil {
				return googleAuthArgs{}, err
			}
			scopes := splitScopes(v)
			if len(scopes) == 0 {
				return googleAuthArgs{}, errors.New("--scopes must list at least one scope")
			}
			out.scopes = scopes
		default:
			return googleAuthArgs{}, errors.New(googleAuthUsage)
		}
	}

	if manual && out.listen != "" {
		return googleAuthArgs{}, errors.New("--manual and --listen cannot be used together")
	}
	if userRaw == "" {
		return googleAuthArgs{}, errors.New(googleAuthUsage)
	}
	id, err := strconv.ParseInt(userRaw, 10, 64)
	if err != nil || id <= 0 {
		return googleAuthArgs{}, fmt.Errorf("user ID must be a positive integer, got %q", userRaw)
	}
	out.user = identity.UserID(id)
	return out, nil
}

func splitScopes(raw string) []string {
	var scopes []string
	for _, part := range strings.Split(raw, ",") {
		if s := strings.TrimSpace(part); s != "" {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

func isLoopbackListenAddr(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func googleAuth(ctx context.Context, st store.Store, sealer *credentials.Sealer, opts googleAuthOptions, stdin io.Reader, stdout io.Writer) error {
	if opts.AuthURL == "" {
		opts.AuthURL = googleAuthURL
	}
	if opts.TokenURL == "" {
		opts.TokenURL = googleTokenURL
	}
	if opts.Key == "" {
		opts.Key = defaultGoogleOAuthKey
	}
	if len(opts.Scopes) == 0 {
		opts.Scopes = defaultGoogleScopes
	}

	state, err := randomHex(32)
	if err != nil {
		return fmt.Errorf("generate oauth state: %w", err)
	}
	verifier := oauth2.GenerateVerifier()

	redirectURI := defaultGoogleRedirectURI
	var ln net.Listener
	if opts.ListenAddr != "" {
		ln, err = net.Listen("tcp", opts.ListenAddr)
		if err != nil {
			return fmt.Errorf("listen for oauth callback: %w", err)
		}
		defer ln.Close()
		redirectURI = fmt.Sprintf("http://%s/callback", ln.Addr().String())
	}

	oc := &oauth2.Config{
		ClientID:     opts.ClientID,
		ClientSecret: opts.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  opts.AuthURL,
			TokenURL: opts.TokenURL,
		},
		RedirectURL: redirectURI,
		Scopes:      opts.Scopes,
	}

	authURL := oc.AuthCodeURL(
		state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier),
	)

	var code, gotState string
	if ln == nil {
		if _, err := fmt.Fprintf(stdout, "Open this URL in a browser, approve access, then paste the redirected callback URL:\n%s\n\nCallback URL: ", authURL); err != nil {
			return err
		}
		code, gotState, err = readManualCallback(stdin)
	} else {
		if _, err := fmt.Fprintf(stdout, "Open this URL in a browser to authorize access:\n%s\n\nWaiting for callback on %s...\n", authURL, redirectURI); err != nil {
			return err
		}
		code, gotState, err = waitLoopbackCallback(ctx, ln)
	}
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(gotState), []byte(state)) != 1 {
		return errors.New("oauth state mismatch")
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("callback URL is missing authorization code")
	}

	exchangeCtx := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: googleExchangeTimeout})
	tok, err := oc.Exchange(exchangeCtx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return sanitizeExchangeErr(err)
	}
	payload, err := mcp.MarshalOAuthCredential(tok.RefreshToken)
	if err != nil {
		return errors.New("oauth token response is missing refresh_token; re-run with prompt=consent")
	}

	ciphertext, nonce, err := sealer.Seal(opts.User, opts.Key, payload)
	if err != nil {
		return fmt.Errorf("encrypt credential: %w", err)
	}
	if err := st.SetCredential(identity.WithUser(ctx, opts.User), opts.User, opts.Key, ciphertext, nonce); err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "stored credential %s for user %d\n", opts.Key, opts.User)
	return err
}

func readManualCallback(r io.Reader) (code, state string, err error) {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", "", fmt.Errorf("read callback URL: %w", err)
		}
		return "", "", errors.New("no callback URL provided on stdin")
	}
	raw := strings.TrimSpace(sc.Text())
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return "", "", errors.New("invalid callback URL")
	}
	q := u.Query()
	if errCode := q.Get("error"); errCode != "" {
		return "", "", fmt.Errorf("oauth authorization failed (%s)", errCode)
	}
	return q.Get("code"), q.Get("state"), nil
}

func waitLoopbackCallback(ctx context.Context, ln net.Listener) (code, state string, err error) {
	type result struct {
		code  string
		state string
		err   error
	}
	ch := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if errCode := q.Get("error"); errCode != "" {
			http.Error(w, "Authorization failed.", http.StatusBadRequest)
			select {
			case ch <- result{err: fmt.Errorf("oauth authorization failed (%s)", errCode)}:
			default:
			}
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Authorization received. You can close this window.\n")
		select {
		case ch <- result{code: q.Get("code"), state: q.Get("state")}:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	case res := <-ch:
		return res.code, res.state, res.err
	}
}

func randomHex(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sanitizeExchangeErr(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil {
		if re.ErrorCode != "" {
			return fmt.Errorf("oauth token exchange failed (status %d, code %s)", re.Response.StatusCode, re.ErrorCode)
		}
		return fmt.Errorf("oauth token exchange failed (status %d)", re.Response.StatusCode)
	}
	return errors.New("oauth token exchange failed")
}
