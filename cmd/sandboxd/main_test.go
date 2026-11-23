package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/sandbox"
)

// mountEngine answers MountSource only; the rest of sandbox.Engine is unused here.
type mountEngine struct {
	sandbox.Engine
	source string
	err    error
}

func (m mountEngine) MountSource(_ context.Context, container, destination string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.source + "|" + container + "|" + destination, nil
}

func TestHostSkillsDir(t *testing.T) {
	tests := []struct {
		name        string
		inContainer bool
		engine      mountEngine
		want        string
		wantErr     string
	}{
		{"outside a container the folder is used as is", false, mountEngine{err: errors.New("unused")}, "/etc/nestor/skills", ""},
		{"inside a container the mount source is used", true, mountEngine{source: "/srv/nestor/skills"}, "/srv/nestor/skills|host1|/etc/nestor/skills", ""},
		{"inside a container a failed lookup stops startup", true, mountEngine{err: errors.New("no such container")}, "", "find host path of /etc/nestor/skills"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hostSkillsDir(context.Background(), tt.engine, "/etc/nestor/skills", "host1", tt.inContainer)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("hostSkillsDir() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestLoadSettings(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "skills.yaml")
	if err := os.WriteFile(cfgPath, []byte("skills: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		addr    string
	}{
		{"defaults", map[string]string{"SKILLS_CONFIG": cfgPath, "SKILLS_DIR": "/s"}, "", ":8080"},
		{"address set", map[string]string{"SKILLS_CONFIG": cfgPath, "SKILLS_DIR": "/s", "SANDBOXD_ADDR": ":9000"}, "", ":9000"},
		{"config required", map[string]string{"SKILLS_DIR": "/s"}, "SKILLS_CONFIG is required", ""},
		{"dir required", map[string]string{"SKILLS_CONFIG": cfgPath}, "SKILLS_DIR is required", ""},
		{"invalid config", map[string]string{"SKILLS_CONFIG": cfgPath + ".missing", "SKILLS_DIR": "/s"}, "open skills config", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := loadSettings(func(k string) string { return tt.env[k] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || s.addr != tt.addr || s.skillsDir != "/s" {
				t.Fatalf("loadSettings() = %+v, %v", s, err)
			}
		})
	}
}
