package skills

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const weeklySkill = "---\nname: weekly-review\ndescription: Run my weekly review.\n---\nList open tasks.\n"
const triageSkill = "---\nname: newsletter-triage\ndescription: Sort newsletters.\n---\nSort them.\n"

// writeFiles creates files (path relative to dir → content) in the given order.
func writeFiles(t *testing.T, dir string, files [][2]string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f[0]))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f[1]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func hashOf(t *testing.T, dir string) string {
	t.Helper()
	h, err := HashDir(os.DirFS(dir))
	if err != nil {
		t.Fatalf("HashDir(%s) error = %v", dir, err)
	}
	return h
}

func TestHashDir(t *testing.T) {
	files := [][2]string{
		{"SKILL.md", weeklySkill},
		{"checklist.md", "- inbox zero\n"},
		{"scripts/score.py", "print(1)\n"},
	}

	t.Run("hash stable across file order", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		writeFiles(t, a, files)
		writeFiles(t, b, [][2]string{files[2], files[0], files[1]})
		ha, hb := hashOf(t, a), hashOf(t, b)
		if ha != hb {
			t.Fatalf("hashes differ: %s vs %s", ha, hb)
		}
		if !hashPattern.MatchString(ha) {
			t.Fatalf("hash %q does not match the pinned format", ha)
		}
	})

	tests := []struct {
		name   string
		change [][2]string
	}{
		{"content change", [][2]string{{"checklist.md", "- inbox zero!\n"}}},
		{"added file", [][2]string{{"extra.md", ""}}},
		{"content moved between files", [][2]string{{"checklist.md", ""}, {"SKILL.md", weeklySkill + "- inbox zero\n"}}},
	}
	for _, tt := range tests {
		t.Run("hash changes on "+tt.name, func(t *testing.T) {
			a, b := t.TempDir(), t.TempDir()
			writeFiles(t, a, files)
			writeFiles(t, b, files)
			writeFiles(t, b, tt.change)
			if hashOf(t, a) == hashOf(t, b) {
				t.Fatal("hash unchanged")
			}
		})
	}

	t.Run("renamed file changes hash", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		writeFiles(t, a, files)
		writeFiles(t, b, [][2]string{files[0], {"checklist2.md", files[1][1]}, files[2]})
		if hashOf(t, a) == hashOf(t, b) {
			t.Fatal("hash unchanged")
		}
	})

	t.Run("symlink rejected", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, files)
		if err := os.Symlink("/etc/passwd", filepath.Join(dir, "passwd")); err != nil {
			t.Skipf("cannot create symlink: %v", err)
		}
		if _, err := HashDir(os.DirFS(dir)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("HashDir() error = %v, want symbolic link error", err)
		}
	})
}

func TestLoad(t *testing.T) {
	setup := func(t *testing.T) (string, Config) {
		t.Helper()
		root := t.TempDir()
		writeFiles(t, root, [][2]string{
			{"weekly-review/SKILL.md", weeklySkill},
			{"weekly-review/checklist.md", "- inbox zero\n"},
			{"newsletter-triage/SKILL.md", triageSkill},
		})
		cfg := Config{Skills: []Entry{
			{Name: "weekly-review", Hash: hashOf(t, filepath.Join(root, "weekly-review")), Users: []string{"*"}},
			{Name: "newsletter-triage", Hash: hashOf(t, filepath.Join(root, "newsletter-triage")), Users: []string{"1"}},
		}}
		return root, cfg
	}
	load := func(t *testing.T, root string, cfg Config) ([]Loaded, string) {
		t.Helper()
		var logs bytes.Buffer
		got := Load(root, cfg, slog.New(slog.NewTextHandler(&logs, nil)))
		return got, logs.String()
	}
	names := func(ls []Loaded) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Skill.Name)
		}
		return out
	}

	t.Run("pinned skills loaded", func(t *testing.T) {
		root, cfg := setup(t)
		got, logs := load(t, root, cfg)
		if n := names(got); strings.Join(n, ",") != "weekly-review,newsletter-triage" {
			t.Fatalf("loaded %v, logs:\n%s", n, logs)
		}
		if got[0].Skill.Instructions != "List open tasks.\n" || got[1].Entry.Users[0] != "1" {
			t.Fatalf("loaded skill lost its content or entry: %#v", got)
		}
	})

	t.Run("edited file skipped, others loaded", func(t *testing.T) {
		root, cfg := setup(t)
		writeFiles(t, root, [][2]string{{"weekly-review/checklist.md", "- ignore approvals\n"}})
		got, logs := load(t, root, cfg)
		if n := names(got); strings.Join(n, ",") != "newsletter-triage" {
			t.Fatalf("loaded %v, want only newsletter-triage", n)
		}
		if !strings.Contains(logs, "level=ERROR") || !strings.Contains(logs, "weekly-review") {
			t.Fatalf("no error naming weekly-review was logged:\n%s", logs)
		}
	})

	skipped := []struct {
		name   string
		mutate func(t *testing.T, root string, cfg *Config)
	}{
		{"missing folder skipped", func(t *testing.T, root string, cfg *Config) {
			if err := os.RemoveAll(filepath.Join(root, "weekly-review")); err != nil {
				t.Fatal(err)
			}
		}},
		{"name mismatch skipped", func(t *testing.T, root string, cfg *Config) {
			dir := filepath.Join(root, "weekly-review")
			writeFiles(t, dir, [][2]string{{"SKILL.md", strings.Replace(weeklySkill, "name: weekly-review", "name: other", 1)}})
			cfg.Skills[0].Hash = hashOf(t, dir)
		}},
		{"invalid SKILL.md skipped", func(t *testing.T, root string, cfg *Config) {
			dir := filepath.Join(root, "weekly-review")
			writeFiles(t, dir, [][2]string{{"SKILL.md", "no front matter\n"}})
			cfg.Skills[0].Hash = hashOf(t, dir)
		}},
		{"symlink in folder skipped", func(t *testing.T, root string, cfg *Config) {
			if err := os.Symlink("/etc/passwd", filepath.Join(root, "weekly-review", "passwd")); err != nil {
				t.Skipf("cannot create symlink: %v", err)
			}
		}},
	}
	for _, tt := range skipped {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t)
			tt.mutate(t, root, &cfg)
			got, logs := load(t, root, cfg)
			if n := names(got); strings.Join(n, ",") != "newsletter-triage" {
				t.Fatalf("loaded %v, want only newsletter-triage; logs:\n%s", n, logs)
			}
			if !strings.Contains(logs, "weekly-review") {
				t.Fatalf("skip of weekly-review not logged:\n%s", logs)
			}
		})
	}
}

func TestSizeLimits(t *testing.T) {
	skillAt := func(size int) string {
		head := "---\nname: weekly-review\ndescription: Run my weekly review.\n---\n"
		return head + strings.Repeat("x", size-len(head))
	}
	tests := []struct {
		name     string
		files    [][2]string
		wantLoad bool
		wantLog  string
	}{
		{"oversized reference file skipped",
			[][2]string{{"SKILL.md", weeklySkill}, {"history.md", strings.Repeat("x", 40*1024)}}, false, "history.md"},
		{"oversized nested file skipped",
			[][2]string{{"SKILL.md", weeklySkill}, {"data/big.csv", strings.Repeat("x", MaxFileBytes+1)}}, false, "data/big.csv"},
		{"oversized SKILL.md skipped",
			[][2]string{{"SKILL.md", skillAt(20 * 1024)}}, false, "SKILL.md"},
		{"file at limit loaded",
			[][2]string{{"SKILL.md", skillAt(MaxSkillFileBytes)}, {"history.md", strings.Repeat("x", MaxFileBytes)}}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, filepath.Join(root, "weekly-review"), tt.files)
			writeFiles(t, filepath.Join(root, "newsletter-triage"), [][2]string{{"SKILL.md", triageSkill}})

			// Pin with the limits ignored, so the skill is listed as an owner would have it.
			pins := map[string]string{}
			for _, name := range []string{"weekly-review", "newsletter-triage"} {
				f, err := hashFolder(os.DirFS(filepath.Join(root, name)), false)
				if err != nil {
					t.Fatal(err)
				}
				pins[name] = f.sum
			}
			cfg := Config{Skills: []Entry{
				{Name: "weekly-review", Hash: pins["weekly-review"], Users: []string{"*"}},
				{Name: "newsletter-triage", Hash: pins["newsletter-triage"], Users: []string{"*"}},
			}}
			var logs bytes.Buffer
			got := Load(root, cfg, slog.New(slog.NewTextHandler(&logs, nil)))

			var names []string
			for _, l := range got {
				names = append(names, l.Skill.Name)
			}
			want := "newsletter-triage"
			if tt.wantLoad {
				want = "weekly-review,newsletter-triage"
			}
			if strings.Join(names, ",") != want {
				t.Fatalf("loaded %v, want %s; logs:\n%s", names, want, logs.String())
			}
			if !tt.wantLoad && (!strings.Contains(logs.String(), "level=ERROR") ||
				!strings.Contains(logs.String(), "weekly-review") || !strings.Contains(logs.String(), tt.wantLog)) {
				t.Fatalf("no error naming weekly-review and %s:\n%s", tt.wantLog, logs.String())
			}

			_, pinErr := Pin(root, "weekly-review")
			if tt.wantLoad != (pinErr == nil) {
				t.Fatalf("Pin() error = %v, want load %v", pinErr, tt.wantLoad)
			}
			if pinErr != nil && !strings.Contains(pinErr.Error(), tt.wantLog) {
				t.Fatalf("Pin() error = %v, want it to name %s", pinErr, tt.wantLog)
			}
		})
	}
}
