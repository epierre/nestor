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
		Networks    []string `yaml:"networks"`
		ReadOnly    bool     `yaml:"read_only"`
		User        string   `yaml:"user"`
		CapDrop     []string `yaml:"cap_drop"`
		SecurityOpt []string `yaml:"security_opt"`
		Ports       []any    `yaml:"ports"`
		Profiles    []string `yaml:"profiles"`
		NetworkMode string   `yaml:"network_mode"`
		Volumes     []string `yaml:"volumes"`
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
