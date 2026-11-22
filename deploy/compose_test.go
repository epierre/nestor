package deploy_test

import (
	"os"
	"slices"
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
