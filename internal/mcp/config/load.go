package config

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"

	"github.com/epierre/nestor/internal/identity"
)

// Load decodes mcp.yaml from r and validates it against knownUsers. Unknown keys are rejected so
// a misspelled field fails at startup instead of being silently ignored.
func Load(r io.Reader, knownUsers []identity.UserID) (File, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var f File
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("decode mcp.yaml: %w", err)
	}
	if err := Validate(f, knownUsers); err != nil {
		return File{}, fmt.Errorf("invalid mcp.yaml: %w", err)
	}
	return f, nil
}
