package skills

import (
	"strings"

	"github.com/epierre/nestor/internal/identity"
)

// MaxPromptBlockBytes caps the skills block added to the system prompt, so adding skills cannot
// crowd out the conversation. Skills that do not fit are left out of the block, in skills.yaml
// order; each line is either listed whole or not at all.
const MaxPromptBlockBytes = 4096

const promptHeader = "Available skills (call load_skill to use one):\n"

// Catalog holds the loaded skills and answers which of them a user may see.
type Catalog struct {
	skills []Loaded
}

// NewCatalog returns a catalog of loaded, in the order given.
func NewCatalog(loaded []Loaded) *Catalog {
	return &Catalog{skills: loaded}
}

// PromptBlock returns the system prompt block listing the name and description of each skill
// enabled for user, or "" when there is none. Instructions are never included: they reach the
// model only through load_skill.
func (c *Catalog) PromptBlock(user identity.UserID) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	for _, l := range c.skills {
		if !l.Entry.EnabledFor(user) {
			continue
		}
		line := "- " + l.Skill.Name + ": " + oneLine(l.Skill.Description) + "\n"
		if b.Len() == 0 {
			b.WriteString(promptHeader)
		}
		if b.Len()+len(line) > MaxPromptBlockBytes {
			break
		}
		b.WriteString(line)
	}
	if b.Len() <= len(promptHeader) {
		return ""
	}
	return b.String()
}

// oneLine collapses all whitespace, newlines included, so a description stays on its skill's line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
