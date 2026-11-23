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
	big := filepath.Join(root, "big-skill")
	if err := os.MkdirAll(big, 0o755); err != nil {
		t.Fatal(err)
	}
	bigSkill := "---\nname: big-skill\ndescription: Too big.\n---\nRead history.md.\n"
	if err := os.WriteFile(filepath.Join(big, "SKILL.md"), []byte(bigSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(big, "history.md"), bytes.Repeat([]byte("x"), 40*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	bigMD := filepath.Join(root, "big-instructions")
	if err := os.MkdirAll(bigMD, 0o755); err != nil {
		t.Fatal(err)
	}
	bigMDSkill := "---\nname: big-instructions\ndescription: Too long.\n---\n" + strings.Repeat("x", 20*1024)
	if err := os.WriteFile(filepath.Join(bigMD, "SKILL.md"), []byte(bigMDSkill), 0o644); err != nil {
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
		{"pin refuses oversized file", []string{"pin", "big-skill"}, "", "history.md"},
		{"pin refuses oversized SKILL.md", []string{"pin", "big-instructions"}, "", "SKILL.md"},
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
