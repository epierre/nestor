package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/skills"
)

func TestSkillsCommand(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "weekly-review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: weekly-review\ndescription: Run my weekly review.\n---\nList open tasks.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	want, err := skills.HashDir(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		args    []string
		wantOut string
		wantErr string
	}{
		{"pin prints the hash", []string{"pin", "weekly-review"}, want, ""},
		{"unknown skill", []string{"pin", "nope"}, "", "nope"},
		{"invalid name", []string{"pin", "../etc"}, "", "invalid skill name"},
		{"missing name", []string{"pin"}, "", "usage"},
		{"unknown subcommand", []string{"enable", "weekly-review"}, "", "usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := skillsCommand(root, tt.args, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(out.String(), "hash: "+tt.wantOut) {
				t.Errorf("output %q lacks %q", out.String(), "hash: "+tt.wantOut)
			}
		})
	}
}
