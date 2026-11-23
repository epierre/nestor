package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/epierre/nestor/internal/skills"
)

const skillsUsage = "usage: nestor skills pin <name>"

// skillsDir is where reviewed skills are vendored, relative to the repository root. Pinning is
// done there, after review, before the hash is committed to config/skills.yaml.
const skillsDir = "skills"

// skillsCommand runs one skills subcommand. Only "pin" exists: it prints the content hash of a
// reviewed skill folder under root, for the owner to copy into skills.yaml.
func skillsCommand(root string, args []string, w io.Writer) error {
	if len(args) != 2 || args[0] != "pin" {
		return errors.New(skillsUsage)
	}
	name := args[1]
	sum, err := skills.Pin(root, name)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "  - name: %s\n    hash: %s\n\nAdd this entry to config/skills.yaml after reviewing every file in %s/%s.\n",
		name, sum, root, name)
	return err
}
