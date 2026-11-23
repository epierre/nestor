// Package skills loads reviewed, pinned skills: folders with a SKILL.md (front matter with a
// name and description, then instructions) listed in skills.yaml. Skill content is data; it can
// never register tools, change risk levels or grant approvals.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/epierre/nestor/internal/identity"
)

// MaxDescriptionRunes caps a skill description, so adding skills cannot crowd the system prompt.
const MaxDescriptionRunes = 200

// namePattern is the open SKILL.md name format: lowercase words joined by single hyphens. It
// also keeps names safe to use as folder names (no separators, no dots).
var namePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const maxNameLen = 64

// hashPattern is the pinned content hash format written by `nestor skills pin`.
var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Skill is a parsed SKILL.md.
type Skill struct {
	Name         string
	Description  string
	Instructions string // the body after the front matter
}

// frontMatter holds the SKILL.md fields Nestor uses. Other fields of the open format (license,
// allowed-tools, metadata) are ignored on purpose: they have no effect here.
type frontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// ParseSkill parses the content of a SKILL.md file.
func ParseSkill(data []byte) (Skill, error) {
	raw, body, err := splitFrontMatter(data)
	if err != nil {
		return Skill{}, err
	}

	var fm frontMatter
	if err := yaml.Unmarshal(raw, &fm); err != nil {
		return Skill{}, fmt.Errorf("parse SKILL.md front matter: %w", err)
	}

	s := Skill{
		Name:         strings.TrimSpace(fm.Name),
		Description:  strings.TrimSpace(fm.Description),
		Instructions: strings.TrimLeft(string(body), "\r\n"),
	}
	switch {
	case s.Name == "":
		return Skill{}, errors.New("SKILL.md has no name")
	case !validName(s.Name):
		return Skill{}, fmt.Errorf("SKILL.md has invalid name %q", s.Name)
	case s.Description == "":
		return Skill{}, fmt.Errorf("skill %q has no description", s.Name)
	case utf8.RuneCountInString(s.Description) > MaxDescriptionRunes:
		return Skill{}, fmt.Errorf("skill %q description is longer than %d characters", s.Name, MaxDescriptionRunes)
	case strings.TrimSpace(s.Instructions) == "":
		return Skill{}, fmt.Errorf("skill %q has no instructions", s.Name)
	}
	return s, nil
}

// splitFrontMatter returns the YAML between the opening and closing "---" lines and the rest.
func splitFrontMatter(data []byte) (frontMatter, body []byte, err error) {
	first, rest, ok := cutLine(data)
	if !ok || !isDelimiter(first) {
		return nil, nil, errors.New("SKILL.md does not start with front matter")
	}
	start := len(data) - len(rest)
	for len(rest) > 0 {
		line, next, _ := cutLine(rest)
		if isDelimiter(line) {
			end := len(data) - len(rest)
			return data[start:end], next, nil
		}
		rest = next
	}
	return nil, nil, errors.New("SKILL.md front matter is not closed")
}

// cutLine splits off the first line (without its newline); ok is false for empty input.
func cutLine(data []byte) (line, rest []byte, ok bool) {
	if len(data) == 0 {
		return nil, nil, false
	}
	line, rest, _ = bytes.Cut(data, []byte("\n"))
	return line, rest, true
}

func isDelimiter(line []byte) bool {
	return string(bytes.TrimRight(line, " \t\r")) == "---"
}

func validName(name string) bool {
	return len(name) <= maxNameLen && namePattern.MatchString(name)
}

// Config is the parsed content of skills.yaml.
type Config struct {
	Skills []Entry `yaml:"skills"`
}

// Entry enables one reviewed skill.
type Entry struct {
	Name         string       `yaml:"name"`
	Hash         string       `yaml:"hash"`  // sha256 over the skill folder, from `nestor skills pin`
	Users        []string     `yaml:"users"` // internal user IDs, or "*" for every user
	Scripts      bool         `yaml:"scripts"`
	ScriptLimits ScriptLimits `yaml:"script_limits"`
}

// ScriptLimits bounds one sandboxed script run.
type ScriptLimits struct {
	Timeout        time.Duration `yaml:"timeout"`
	Memory         string        `yaml:"memory"` // passed to the sandbox, such as "256m"
	MaxOutputBytes int           `yaml:"max_output_bytes"`
}

// LoadConfig decodes skills.yaml from r and validates it against knownUsers. Unknown keys are
// rejected so a misspelled field fails at startup instead of being silently ignored.
func LoadConfig(r io.Reader, knownUsers []identity.UserID) (Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)

	var c Config
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode skills.yaml: %w", err)
	}
	if err := validateConfig(c, knownUsers); err != nil {
		return Config{}, fmt.Errorf("invalid skills.yaml: %w", err)
	}
	return c, nil
}

func validateConfig(c Config, knownUsers []identity.UserID) error {
	var errs []error
	seen := make(map[string]bool, len(c.Skills))
	for _, e := range c.Skills {
		fail := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("skill %q "+format, append([]any{e.Name}, args...)...))
		}
		if !validName(e.Name) {
			errs = append(errs, fmt.Errorf("skills.yaml has invalid name %q", e.Name))
			continue
		}
		if seen[e.Name] {
			fail("is listed twice")
		}
		seen[e.Name] = true

		switch {
		case e.Hash == "":
			fail("is not pinned (no hash)")
		case !hashPattern.MatchString(e.Hash):
			fail("has malformed hash %q", e.Hash)
		}
		for _, u := range e.Users {
			if u != "*" && !isKnownUser(u, knownUsers) {
				fail("refers to unknown user %q", u)
			}
		}
		if !e.Scripts && e.ScriptLimits != (ScriptLimits{}) {
			fail("sets script_limits but scripts are disabled")
		}
		if e.ScriptLimits.Timeout < 0 || e.ScriptLimits.MaxOutputBytes < 0 {
			fail("has negative script limits")
		}
	}
	return errors.Join(errs...)
}

// EnabledFor reports whether the entry's users list includes user; "*" includes everyone.
func (e Entry) EnabledFor(user identity.UserID) bool {
	for _, u := range e.Users {
		if u == "*" {
			return true
		}
		if id, err := strconv.ParseInt(u, 10, 64); err == nil && identity.UserID(id) == user {
			return true
		}
	}
	return false
}

func isKnownUser(raw string, knownUsers []identity.UserID) bool {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return false
	}
	return slices.Contains(knownUsers, identity.UserID(id))
}
