package deploy_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type compose struct {
	Services map[string]struct {
		Environment map[string]string `yaml:"environment"`
		Networks    []string          `yaml:"networks"`
		ReadOnly    bool              `yaml:"read_only"`
		User        string            `yaml:"user"`
		CapDrop     []string          `yaml:"cap_drop"`
		SecurityOpt []string          `yaml:"security_opt"`
		Ports       []any             `yaml:"ports"`
		Profiles    []string          `yaml:"profiles"`
		NetworkMode string            `yaml:"network_mode"`
		Volumes     []string          `yaml:"volumes"`
	} `yaml:"services"`
	Networks map[string]struct {
		Internal bool `yaml:"internal"`
	} `yaml:"networks"`
}

// TestMCPServersIsolated checks the isolation of MCP server containers: every service on the mcp
// network other than Nestor is an MCP server, and it may reach nothing but Nestor.
func TestMCPServersIsolated(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	var c compose
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse compose file: %v", err)
	}

	if net, ok := c.Networks["mcp"]; !ok || !net.Internal {
		t.Fatal("network mcp missing or not internal (it must have no route out)")
	}
	if !slices.Contains(c.Services["nestor"].Networks, "mcp") {
		t.Error("nestor is not on the mcp network")
	}
	for _, name := range []string{"postgres", "litellm"} {
		if slices.Contains(c.Services[name].Networks, "mcp") {
			t.Errorf("%s is on the mcp network", name)
		}
	}

	servers := 0
	for name, s := range c.Services {
		if name == "nestor" || !slices.Contains(s.Networks, "mcp") {
			continue
		}
		servers++
		if !slices.Equal(s.Networks, []string{"mcp"}) {
			t.Errorf("%s: networks %v, want only mcp", name, s.Networks)
		}
		if !s.ReadOnly {
			t.Errorf("%s: root filesystem is writable", name)
		}
		if s.User == "" || s.User == "0" || s.User == "root" || s.User[:2] == "0:" {
			t.Errorf("%s: user %q, want a non-root user", name, s.User)
		}
		if !slices.Contains(s.CapDrop, "ALL") {
			t.Errorf("%s: capabilities not dropped", name)
		}
		if !slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
			t.Errorf("%s: no-new-privileges not set", name)
		}
		if len(s.Ports) != 0 {
			t.Errorf("%s: publishes ports %v", name, s.Ports)
		}
	}
	if servers == 0 {
		t.Error("no MCP server service found on the mcp network")
	}
}

// TestSandboxdIsolated checks that sandboxd, which holds the Docker socket, is reachable only by
// Nestor, reaches nothing else, and that it is the only service given the socket.
func TestSandboxdIsolated(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	var c compose
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse compose file: %v", err)
	}

	if net, ok := c.Networks["sandbox"]; !ok || !net.Internal {
		t.Fatal("network sandbox missing or not internal (it must have no route out)")
	}
	for name, s := range c.Services {
		onSandbox := slices.Contains(s.Networks, "sandbox")
		if onSandbox && name != "nestor" && name != "sandboxd" {
			t.Errorf("%s is on the sandbox network", name)
		}
		for _, v := range s.Volumes {
			if strings.Contains(v, "docker.sock") && name != "sandboxd" {
				t.Errorf("%s mounts the Docker socket", name)
			}
		}
	}
	if !slices.Contains(c.Services["nestor"].Networks, "sandbox") {
		t.Error("nestor is not on the sandbox network")
	}

	s, ok := c.Services["sandboxd"]
	if !ok {
		t.Fatal("no sandboxd service")
	}
	if !slices.Equal(s.Networks, []string{"sandbox"}) {
		t.Errorf("sandboxd: networks %v, want only sandbox (no Postgres, no LiteLLM, no route out)", s.Networks)
	}
	if len(s.Ports) != 0 {
		t.Errorf("sandboxd: publishes ports %v", s.Ports)
	}
	if !s.ReadOnly {
		t.Error("sandboxd: root filesystem is writable")
	}
	if !slices.Contains(s.CapDrop, "ALL") {
		t.Error("sandboxd: capabilities not dropped")
	}
	if !slices.Contains(s.SecurityOpt, "no-new-privileges:true") {
		t.Error("sandboxd: no-new-privileges not set")
	}
	for _, v := range s.Volumes {
		if !strings.Contains(v, "docker.sock") && !strings.HasSuffix(v, ":ro") {
			t.Errorf("sandboxd: volume %q is not read-only", v)
		}
	}

	r, ok := c.Services["skill-runner"]
	if !ok {
		t.Fatal("no skill-runner service to build the runner image")
	}
	if !slices.Equal(r.Profiles, []string{"build"}) || r.NetworkMode != "none" || len(r.Networks) != 0 {
		t.Errorf("skill-runner: profiles %v, network_mode %q, networks %v; want build-only with no network",
			r.Profiles, r.NetworkMode, r.Networks)
	}
}

func TestGoogleMapsKeyScopedToNestor(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	var c compose
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse compose file: %v", err)
	}

	nestor, ok := c.Services["nestor"]
	if !ok {
		t.Fatal("missing nestor service")
	}
	if got := nestor.Environment["GOOGLE_MAPS_API_KEY"]; got != "${GOOGLE_MAPS_API_KEY:-}" {
		t.Errorf("nestor GOOGLE_MAPS_API_KEY = %q, want ${GOOGLE_MAPS_API_KEY:-}", got)
	}
	for name, s := range c.Services {
		if name == "nestor" {
			continue
		}
		if _, exists := s.Environment["GOOGLE_MAPS_API_KEY"]; exists {
			t.Errorf("service %q must not define GOOGLE_MAPS_API_KEY", name)
		}
	}
}

func TestGoogleOAuthEnvScopedToNestor(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	var c compose
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse compose file: %v", err)
	}

	nestor, ok := c.Services["nestor"]
	if !ok {
		t.Fatal("missing nestor service")
	}
	wantEnv := map[string]string{
		"GOOGLE_OAUTH_CLIENT_ID":     "${GOOGLE_OAUTH_CLIENT_ID:-}",
		"GOOGLE_OAUTH_CLIENT_SECRET": "${GOOGLE_OAUTH_CLIENT_SECRET:-}",
	}
	for k, want := range wantEnv {
		if got := nestor.Environment[k]; got != want {
			t.Errorf("nestor %s = %q, want %q", k, got, want)
		}
		for name, s := range c.Services {
			if name == "nestor" {
				continue
			}
			if _, exists := s.Environment[k]; exists {
				t.Errorf("service %q must not define %s", name, k)
			}
		}
	}
}

func TestWebSearchModelScopedToNestor(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	var c compose
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse compose file: %v", err)
	}

	nestor, ok := c.Services["nestor"]
	if !ok {
		t.Fatal("missing nestor service")
	}
	if got := nestor.Environment["WEB_SEARCH_MODEL"]; got != "${WEB_SEARCH_MODEL:-}" {
		t.Errorf("nestor WEB_SEARCH_MODEL = %q, want ${WEB_SEARCH_MODEL:-}", got)
	}
	for name, s := range c.Services {
		if name == "nestor" {
			continue
		}
		if _, exists := s.Environment["WEB_SEARCH_MODEL"]; exists {
			t.Errorf("service %q must not define WEB_SEARCH_MODEL", name)
		}
	}
}

func TestLiteLLMConfigDefinesWebSearchAlias(t *testing.T) {
	raw, err := os.ReadFile("litellm.yaml")
	if err != nil {
		t.Fatalf("read litellm.yaml: %v", err)
	}
	var cfg struct {
		ModelList []struct {
			ModelName     string `yaml:"model_name"`
			LiteLLMParams struct {
				Model            string         `yaml:"model"`
				APIKey           string         `yaml:"api_key"`
				WebSearchOptions map[string]any `yaml:"web_search_options"`
			} `yaml:"litellm_params"`
		} `yaml:"model_list"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse litellm.yaml: %v", err)
	}

	for _, entry := range cfg.ModelList {
		if entry.ModelName != "web-search" {
			continue
		}
		if entry.LiteLLMParams.Model == "" {
			t.Error("web-search litellm_params.model is empty")
		}
		if entry.LiteLLMParams.APIKey != "os.environ/GEMINI_API_KEY" {
			t.Errorf("web-search api_key = %q, want os.environ/GEMINI_API_KEY", entry.LiteLLMParams.APIKey)
		}
		if entry.LiteLLMParams.WebSearchOptions == nil {
			t.Error("web-search litellm_params.web_search_options must be set")
		}
		return
	}
	t.Fatal("litellm.yaml missing web-search entry in model_list")
}

func TestSystemPromptMountedReadOnlyOnNestorOnly(t *testing.T) {
	raw, err := os.ReadFile("docker-compose.yml")
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	var c compose
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse compose file: %v", err)
	}

	nestor, ok := c.Services["nestor"]
	if !ok {
		t.Fatal("missing nestor service")
	}
	const (
		wantEnv = "/etc/nestor/system_prompt.md"
		wantVol = "../config/system_prompt.md:/etc/nestor/system_prompt.md:ro"
	)
	if got := nestor.Environment["SYSTEM_PROMPT_FILE"]; got != wantEnv {
		t.Errorf("nestor SYSTEM_PROMPT_FILE = %q, want %q", got, wantEnv)
	}
	if !slices.Contains(nestor.Volumes, wantVol) {
		t.Errorf("nestor volumes %v missing %q", nestor.Volumes, wantVol)
	}
	for name, s := range c.Services {
		if name == "nestor" {
			continue
		}
		if _, exists := s.Environment["SYSTEM_PROMPT_FILE"]; exists {
			t.Errorf("service %q must not define SYSTEM_PROMPT_FILE", name)
		}
		for _, v := range s.Volumes {
			if strings.Contains(v, "system_prompt.md") {
				t.Errorf("service %q must not mount system_prompt.md (%q)", name, v)
			}
		}
	}
}
