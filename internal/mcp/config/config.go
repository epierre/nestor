// Package config describes the reviewed MCP server configuration (mcp.yaml) and validates it at
// startup. A file that fails validation stops the process before any server is contacted.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

// File is the parsed content of mcp.yaml.
type File struct {
	Servers []Server `yaml:"servers"`
}

// Server is one MCP server the assistant may connect to.
type Server struct {
	Name             string `yaml:"name"`
	Transport        string `yaml:"transport"` // "http" or "stdio"
	URL              string `yaml:"url"`
	Image            string `yaml:"image"`
	Remote           bool   `yaml:"remote"` // true = hosted by a third party, so data leaves the host
	Credentials      string `yaml:"credentials"`
	CredentialKey    string `yaml:"credential_key"`
	CredentialEnv    string `yaml:"credential_env"`
	CredentialHeader string `yaml:"credential_header"`
	// CredentialDelivery says how a per_user token reaches the server: "header" (one shared
	// local instance, token sent on each call), "container" (one instance per user), or "oauth2"
	// (per-user refresh token in user_credentials, refreshed via oauth_token_url).
	CredentialDelivery   string          `yaml:"credential_delivery"`
	OAuthTokenURL        string          `yaml:"oauth_token_url"`
	OAuthClientIDEnv     string          `yaml:"oauth_client_id_env"`
	OAuthClientSecretEnv string          `yaml:"oauth_client_secret_env"`
	Users                []string        `yaml:"users"` // internal user IDs, or "*" for every user
	Timeout            time.Duration   `yaml:"timeout"`
	HealthInterval     time.Duration   `yaml:"health_interval"`
	MaxResultBytes     int             `yaml:"max_result_bytes"`
	Tools              map[string]Tool `yaml:"tools"` // the allowlist: only these tools are exposed
}

// Tool is one allowlisted tool. Its risk comes from here, never from the server.
type Tool struct {
	Risk tools.Risk `yaml:"risk"`
}

// userPlaceholder marks a per-user instance in a server URL, such as
// http://mcp-todoist-{user}:8080/mcp.
const userPlaceholder = "{user}"

// Validate checks f against the rules in openspec/specs/mcp-integration/spec.md. knownUsers lists
// the user IDs that exist. Every problem found is reported at once; the error names servers and
// tools but never a credential value.
func Validate(f File, knownUsers []identity.UserID) error {
	var errs []error
	seen := make(map[string]bool, len(f.Servers))

	for _, s := range f.Servers {
		if seen[s.Name] {
			errs = append(errs, fmt.Errorf("server name %q is not unique", s.Name))
		}
		seen[s.Name] = true

		errs = append(errs, validateServer(s, knownUsers)...)
	}
	return errors.Join(errs...)
}

func validateServer(s Server, knownUsers []identity.UserID) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("server %q "+format, append([]any{s.Name}, args...)...))
	}

	switch s.Transport {
	case "http", "stdio":
	default:
		fail("has unknown transport %q", s.Transport)
	}

	switch s.Credentials {
	case "none":
		if s.CredentialDelivery != "" {
			fail("sets credential_delivery but does not use per_user credentials")
		}
		if s.CredentialEnv != "" {
			fail("sets credential_env but does not use shared credentials")
		}
		if s.CredentialHeader != "" {
			fail("sets credential_header but does not use shared credentials")
		}
	case "shared":
		if s.CredentialDelivery != "" {
			fail("sets credential_delivery but does not use per_user credentials")
		}
		if s.CredentialKey != "" {
			fail("sets credential_key but does not use per_user credentials")
		}
		errs = append(errs, validateShared(s)...)
	case "per_user":
		if s.CredentialEnv != "" {
			fail("sets credential_env but does not use shared credentials")
		}
		if s.CredentialHeader != "" {
			fail("sets credential_header but does not use shared credentials")
		}
		if s.CredentialKey == "" {
			fail("uses per_user credentials but names no credential_key")
		}
		errs = append(errs, validateDelivery(s)...)
	default:
		fail("has unknown credentials %q", s.Credentials)
	}

	if s.CredentialDelivery != "oauth2" && (s.OAuthTokenURL != "" || s.OAuthClientIDEnv != "" || s.OAuthClientSecretEnv != "") {
		fail("sets oauth2 fields but does not use oauth2 credential delivery")
	}

	if s.HealthInterval < 0 {
		fail("has negative health_interval %s", s.HealthInterval)
	}

	for _, name := range sortedKeys(s.Tools) {
		switch risk := s.Tools[name].Risk; risk {
		case tools.RiskRead, tools.RiskReversible, tools.RiskCritical:
		case "":
			errs = append(errs, fmt.Errorf("server %q tool %q has no risk", s.Name, name))
		default:
			errs = append(errs, fmt.Errorf("server %q tool %q has unknown risk %q", s.Name, name, risk))
		}
	}

	for _, u := range s.Users {
		if u == "*" {
			continue
		}
		if !isKnownUser(u, knownUsers) {
			fail("refers to unknown user %q", u)
		}
	}

	if s.Transport == "http" && !s.Remote {
		if host := publicHost(s.URL); host != "" {
			fail("points to public host %q but is not marked remote", host)
		}
	}

	return errs
}

func validateShared(s Server) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("server %q "+format, append([]any{s.Name}, args...)...))
	}
	if s.Transport != "http" {
		fail("uses shared credentials, which needs transport http")
	}
	switch {
	case s.CredentialEnv == "":
		fail("uses shared credentials but names no credential_env")
	case !isValidEnvName(s.CredentialEnv):
		fail("has invalid credential_env %q", s.CredentialEnv)
	}
	if s.CredentialHeader != "" && !isValidHeaderName(s.CredentialHeader) {
		fail("has invalid credential_header %q", s.CredentialHeader)
	}
	if s.Remote && !isHTTPSURL(s.URL) {
		fail("is remote with shared credentials, which needs an https URL")
	}
	return errs
}

// isValidEnvName reports whether s matches ^[A-Z_][A-Z0-9_]*$.
func isValidEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isUpper := c >= 'A' && c <= 'Z'
		isDigit := c >= '0' && c <= '9'
		if i == 0 && !isUpper && c != '_' {
			return false
		}
		if !isUpper && !isDigit && c != '_' {
			return false
		}
	}
	return true
}

// isValidHeaderName reports whether s is a non-empty HTTP field-name token per RFC 9110.
func isValidHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHTTPTokenChar(s[i]) {
			return false
		}
	}
	return true
}

func isHTTPTokenChar(c byte) bool {
	if (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}

func isHTTPSURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

func isHTTPOrHTTPSURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// validateDelivery checks how a per_user server receives its token. A header carries the token
// to a shared instance, so it must stay on the host; a container is started per user.
func validateDelivery(s Server) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("server %q "+format, append([]any{s.Name}, args...)...))
	}
	perUserURL := strings.Contains(s.URL, userPlaceholder)

	switch s.CredentialDelivery {
	case "":
		fail("uses per_user credentials but sets no credential_delivery")
	case "header":
		if s.Remote {
			fail("sends tokens in a header, which needs remote: false")
		}
		if s.Transport != "http" {
			fail("uses header delivery, which needs transport http")
		}
		if perUserURL {
			fail("uses header delivery, so its URL must not contain %s", userPlaceholder)
		}
	case "container":
		if !perUserURL {
			fail("uses container delivery, so its URL must contain %s", userPlaceholder)
		}
	case "oauth2":
		errs = append(errs, validateOAuth2(s, perUserURL)...)
	default:
		fail("has unknown credential_delivery %q", s.CredentialDelivery)
	}
	return errs
}

func validateOAuth2(s Server, perUserURL bool) []error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("server %q "+format, append([]any{s.Name}, args...)...))
	}
	if s.Transport != "http" {
		fail("uses oauth2 delivery, which needs transport http")
	}
	if perUserURL {
		fail("uses oauth2 delivery, so its URL must not contain %s", userPlaceholder)
	}
	if s.Remote && !isHTTPSURL(s.URL) {
		fail("is remote with oauth2 delivery, which needs an https URL")
	}
	switch {
	case s.OAuthTokenURL == "":
		fail("uses oauth2 delivery but names no oauth_token_url")
	case !isHTTPOrHTTPSURL(s.OAuthTokenURL):
		fail("has invalid oauth_token_url %q", s.OAuthTokenURL)
	case s.Remote && !isHTTPSURL(s.OAuthTokenURL):
		fail("is remote with oauth2 delivery, so oauth_token_url needs an https URL")
	}
	switch {
	case s.OAuthClientIDEnv == "":
		fail("uses oauth2 delivery but names no oauth_client_id_env")
	case !isValidEnvName(s.OAuthClientIDEnv):
		fail("has invalid oauth_client_id_env %q", s.OAuthClientIDEnv)
	}
	switch {
	case s.OAuthClientSecretEnv == "":
		fail("uses oauth2 delivery but names no oauth_client_secret_env")
	case !isValidEnvName(s.OAuthClientSecretEnv):
		fail("has invalid oauth_client_secret_env %q", s.OAuthClientSecretEnv)
	}
	return errs
}

// UsersInclude reports whether a server's users list from mcp.yaml includes user: "*" includes
// everyone.
func UsersInclude(users []string, user identity.UserID) bool {
	for _, u := range users {
		if u == "*" {
			return true
		}
		if id, err := strconv.ParseInt(u, 10, 64); err == nil && identity.UserID(id) == user {
			return true
		}
	}
	return false
}

func isKnownUser(raw string, knownUsers []identity.UserID) bool {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	return slices.Contains(knownUsers, identity.UserID(id))
}

// publicHost returns the host of rawURL when it is not internal, or "" when it is. Internal
// means a Compose service name (no dot), localhost, or a loopback, private or link-local address.
// The {user} placeholder is replaced before parsing so per-user URLs are checked too.
func publicHost(rawURL string) string {
	u, err := url.Parse(strings.ReplaceAll(rawURL, userPlaceholder, "0"))
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" || host == "localhost" || !strings.Contains(host, ".") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return ""
		}
	}
	return host
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
