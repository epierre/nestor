// Package prompt loads and validates Nestor's base system prompt from disk.
package prompt

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxBytes is the maximum permitted raw size of a system prompt file.
const MaxBytes = 4096

// Loaded holds a validated, whitespace-trimmed system prompt along with file metadata.
type Loaded struct {
	Text   string
	Bytes  int
	SHA256 string
}

// LoadFile reads, validates, and hashes the regular UTF-8 prompt file at path.
func LoadFile(path string) (Loaded, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return Loaded{}, errors.New("SYSTEM_PROMPT_FILE is required")
	}

	f, err := os.Open(trimmedPath)
	if err != nil {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE: open %q: %w", trimmedPath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE: stat %q: %w", trimmedPath, err)
	}
	if !info.Mode().IsRegular() {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE %q must be a regular file", trimmedPath)
	}
	if info.Size() > MaxBytes {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE %q exceeds %d bytes", trimmedPath, MaxBytes)
	}

	raw, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE: read %q: %w", trimmedPath, err)
	}
	if len(raw) > MaxBytes {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE %q exceeds %d bytes", trimmedPath, MaxBytes)
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE %q contains a NUL byte", trimmedPath)
	}
	if !utf8.Valid(raw) {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE %q is not valid UTF-8", trimmedPath)
	}

	text := strings.TrimSpace(string(raw))
	if text == "" {
		return Loaded{}, fmt.Errorf("SYSTEM_PROMPT_FILE %q is empty", trimmedPath)
	}

	sum := sha256.Sum256(raw)
	return Loaded{
		Text:   text,
		Bytes:  len(raw),
		SHA256: "sha256:" + hex.EncodeToString(sum[:]),
	}, nil
}

// RepoFile locates config/system_prompt.md by walking upward from the working directory to go.mod.
func RepoFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locate repo root: %w", err)
	}
	for {
		info, err := os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil && info.Mode().IsRegular() {
			return filepath.Join(dir, "config", "system_prompt.md"), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("locate repo root: go.mod not found")
		}
		dir = parent
	}
}
