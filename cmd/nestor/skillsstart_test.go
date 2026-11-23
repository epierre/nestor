package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/epierre/nestor/internal/config"
	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/skills"
)

func TestLoadSkills(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "weekly-review")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: weekly-review\ndescription: Run my weekly review.\n---\nList open tasks.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
	hash, err := skills.HashDir(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	writeYAML := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "skills.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	users := []identity.UserID{1, 2}

	t.Run("unset config means no skills", func(t *testing.T) {
		got, err := loadSkills(config.Config{}, users, slog.Default())
		if err != nil || len(got) != 0 {
			t.Fatalf("loadSkills() = %v, %v; want no skills and no error", got, err)
		}
	})

	t.Run("pinned skill loaded", func(t *testing.T) {
		cfg := config.Config{
			SkillsConfigPath: writeYAML(t, "skills:\n  - {name: weekly-review, hash: \""+hash+"\", users: [\"*\"]}\n"),
			SkillsDir:        root,
		}
		got, err := loadSkills(cfg, users, slog.Default())
		if err != nil || len(got) != 1 || got[0].Skill.Name != "weekly-review" {
			t.Fatalf("loadSkills() = %v, %v", got, err)
		}
	})

	t.Run("mismatched skill skipped, startup continues", func(t *testing.T) {
		var logs bytes.Buffer
		cfg := config.Config{
			SkillsConfigPath: writeYAML(t, "skills:\n  - {name: weekly-review, hash: \"sha256:"+strings.Repeat("0", 64)+"\"}\n"),
			SkillsDir:        root,
		}
		got, err := loadSkills(cfg, users, slog.New(slog.NewTextHandler(&logs, nil)))
		if err != nil || len(got) != 0 {
			t.Fatalf("loadSkills() = %v, %v; want no skills and no error", got, err)
		}
		if !strings.Contains(logs.String(), "weekly-review") {
			t.Errorf("skip not logged: %s", logs.String())
		}
	})

	t.Run("invalid skills.yaml stops startup", func(t *testing.T) {
		cfg := config.Config{
			SkillsConfigPath: writeYAML(t, "skills:\n  - {name: weekly-review, hash: \""+hash+"\"}\n  - {name: weekly-review, hash: \""+hash+"\"}\n"),
			SkillsDir:        root,
		}
		_, err := loadSkills(cfg, users, slog.Default())
		if err == nil || !strings.Contains(err.Error(), `skill "weekly-review" is listed twice`) {
			t.Fatalf("loadSkills() error = %v, want the duplicated skill named", err)
		}
	})

	t.Run("missing skills.yaml stops startup", func(t *testing.T) {
		cfg := config.Config{SkillsConfigPath: filepath.Join(root, "absent.yaml"), SkillsDir: root}
		if _, err := loadSkills(cfg, users, slog.Default()); err == nil {
			t.Fatal("loadSkills() succeeded with a missing skills.yaml")
		}
	})
}

func TestShippedSkillsConfigIsValid(t *testing.T) {
	cfg := config.Config{SkillsConfigPath: "../../config/skills.yaml", SkillsDir: "../../skills"}
	var logs bytes.Buffer
	if _, err := loadSkills(cfg, []identity.UserID{1}, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatalf("config/skills.yaml: %v", err)
	}
	if strings.Contains(logs.String(), "skill skipped") {
		t.Errorf("a shipped skill does not match its pin:\n%s", logs.String())
	}
}
