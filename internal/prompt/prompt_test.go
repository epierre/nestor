package prompt

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFile(t *testing.T) {
	validRaw := []byte(" \n# Nestor\n\nStay direct.\n\t ")
	validSum := sha256.Sum256(validRaw)
	wantValidSHA := "sha256:" + hex.EncodeToString(validSum[:])

	exactMaxRaw := []byte(strings.Repeat("a", MaxBytes))
	exactSum := sha256.Sum256(exactMaxRaw)
	wantExactSHA := "sha256:" + hex.EncodeToString(exactSum[:])

	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		wantText    string
		wantBytes   int
		wantSHA256  string
		wantErrSubs []string
	}{
		{
			name: "valid file trims text and hashes raw bytes",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "system_prompt.md")
				if err := os.WriteFile(p, validRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				return "  " + p + "  "
			},
			wantText:   "# Nestor\n\nStay direct.",
			wantBytes:  len(validRaw),
			wantSHA256: wantValidSHA,
		},
		{
			name:        "empty path rejected",
			setup:       func(*testing.T) string { return "" },
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE"},
		},
		{
			name:        "whitespace path rejected",
			setup:       func(*testing.T) string { return "   \t\n" },
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE"},
		},
		{
			name: "missing file rejected",
			setup: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "does-not-exist.md")
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE"},
		},
		{
			name: "directory rejected as non-regular",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE", "regular"},
		},
		{
			name: "empty file rejected",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "empty.md")
				if err := os.WriteFile(p, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE", "empty"},
		},
		{
			name: "whitespace-only file rejected",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "blank.md")
				if err := os.WriteFile(p, []byte(" \t\r\n "), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE", "empty"},
		},
		{
			name: "embedded NUL byte rejected",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "nul.md")
				if err := os.WriteFile(p, []byte("hello\x00world"), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE", "NUL"},
		},
		{
			name: "invalid UTF-8 rejected",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "badutf8.md")
				if err := os.WriteFile(p, []byte{0xff, 0xfe, 'a'}, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE", "UTF-8"},
		},
		{
			name: "exact 4096 bytes succeeds",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "exact.md")
				if err := os.WriteFile(p, exactMaxRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantText:   string(exactMaxRaw),
			wantBytes:  MaxBytes,
			wantSHA256: wantExactSHA,
		},
		{
			name: "4097 bytes rejected",
			setup: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "oversized.md")
				if err := os.WriteFile(p, []byte(strings.Repeat("a", MaxBytes+1)), 0o600); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErrSubs: []string{"SYSTEM_PROMPT_FILE", "4096"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadFile(tt.setup(t))
			if len(tt.wantErrSubs) > 0 {
				if err == nil {
					t.Fatalf("LoadFile succeeded (%+v), want error", got)
				}
				for _, sub := range tt.wantErrSubs {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q does not contain %q", err, sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFile: %v", err)
			}
			if got.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tt.wantText)
			}
			if got.Bytes != tt.wantBytes {
				t.Errorf("Bytes = %d, want %d", got.Bytes, tt.wantBytes)
			}
			if got.SHA256 != tt.wantSHA256 {
				t.Errorf("SHA256 = %q, want %q", got.SHA256, tt.wantSHA256)
			}
		})
	}
}

func TestRepoSystemPrompt(t *testing.T) {
	path, err := RepoFile()
	if err != nil {
		t.Fatalf("RepoFile: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile(%q): %v", path, err)
	}
	if loaded.Bytes <= 0 || loaded.Bytes > MaxBytes {
		t.Errorf("Bytes = %d, want 1..%d", loaded.Bytes, MaxBytes)
	}
	if loaded.Text == "" {
		t.Error("Text is empty")
	}
	if !strings.HasPrefix(loaded.SHA256, "sha256:") {
		t.Errorf("SHA256 = %q, want sha256: prefix", loaded.SHA256)
	}
	if strings.Contains(loaded.Text, "<!--") {
		t.Error("system_prompt.md must not contain HTML comments (<!--)")
	}
}

func TestLoadForEval(t *testing.T) {
	t.Run("uses SYSTEM_PROMPT_FILE when set", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "override.md")
		if err := os.WriteFile(p, []byte("  Custom eval prompt.  "), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := LoadForEval(func(k string) string {
			if k == "SYSTEM_PROMPT_FILE" {
				return " " + p + " "
			}
			return ""
		})
		if err != nil {
			t.Fatalf("LoadForEval: %v", err)
		}
		if got.Text != "Custom eval prompt." {
			t.Errorf("Text = %q, want %q", got.Text, "Custom eval prompt.")
		}
	})

	t.Run("falls back to RepoFile when unset", func(t *testing.T) {
		repoPath, err := RepoFile()
		if err != nil {
			t.Fatalf("RepoFile: %v", err)
		}
		want, err := LoadFile(repoPath)
		if err != nil {
			t.Fatalf("LoadFile: %v", err)
		}
		got, err := LoadForEval(func(string) string { return "  " })
		if err != nil {
			t.Fatalf("LoadForEval: %v", err)
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("returns error when override is missing", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.md")
		_, err := LoadForEval(func(k string) string {
			if k == "SYSTEM_PROMPT_FILE" {
				return missing
			}
			return ""
		})
		if err == nil || !strings.Contains(err.Error(), "SYSTEM_PROMPT_FILE") {
			t.Errorf("err = %v, want error naming SYSTEM_PROMPT_FILE", err)
		}
	})
}
