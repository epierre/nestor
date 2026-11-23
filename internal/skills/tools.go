package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/tools"
)

// lookup returns the skill called name if it is enabled for user. A skill that exists but is not
// enabled for user is reported exactly like one that does not exist.
func (c *Catalog) lookup(user identity.UserID, name string) (Loaded, error) {
	if c != nil {
		for _, l := range c.skills {
			if l.Skill.Name == name && l.Entry.EnabledFor(user) {
				return l, nil
			}
		}
	}
	return Loaded{}, fmt.Errorf("unknown skill %q", name)
}

// RegisterTools adds load_skill and read_skill_file to reg. Both read only the catalog, which was
// built from reviewed configuration; nothing a skill contains can reach the registry.
func RegisterTools(reg *tools.Registry, c *Catalog) error {
	for _, t := range []tools.Tool{NewLoadSkillTool(c), NewReadSkillFileTool(c)} {
		if err := reg.Register(t); err != nil {
			return fmt.Errorf("register %s: %w", t.Name(), err)
		}
	}
	return nil
}

// LoadSkillTool returns the instructions of a skill enabled for the requesting user.
type LoadSkillTool struct{ catalog *Catalog }

// NewLoadSkillTool returns a load_skill tool reading from c.
func NewLoadSkillTool(c *Catalog) *LoadSkillTool { return &LoadSkillTool{catalog: c} }

func (t *LoadSkillTool) Name() string { return "load_skill" }

func (t *LoadSkillTool) Description() string {
	return "Returns the full instructions of one of the available skills listed in the system prompt."
}

func (t *LoadSkillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name, as listed in the system prompt"}
		},
		"required": ["name"],
		"additionalProperties": false
	}`)
}

func (t *LoadSkillTool) Risk() tools.Risk { return tools.RiskRead }

func (t *LoadSkillTool) Preview(args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("%w: %v", tools.ErrValidation, err)
	}
	return fmt.Sprintf("Load skill %s", a.Name), nil
}

func (t *LoadSkillTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return tools.Result{}, fmt.Errorf("load_skill: %w", err)
	}
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("%w: %v", tools.ErrValidation, err)
	}
	l, err := t.catalog.lookup(user, a.Name)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: fmt.Sprintf("Instructions of skill %q (reviewed and pinned):\n\n%s", l.Skill.Name, l.Skill.Instructions)}, nil
}

// ReadSkillFileTool returns a reference file bundled with a skill enabled for the requesting
// user. It serves only regular files that were in the skill's folder when it was loaded and still
// have the content that was pinned.
type ReadSkillFileTool struct{ catalog *Catalog }

// NewReadSkillFileTool returns a read_skill_file tool reading from c.
func NewReadSkillFileTool(c *Catalog) *ReadSkillFileTool { return &ReadSkillFileTool{catalog: c} }

func (t *ReadSkillFileTool) Name() string { return "read_skill_file" }

func (t *ReadSkillFileTool) Description() string {
	return "Returns a reference file bundled with a skill, when the skill's instructions mention it."
}

func (t *ReadSkillFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "Skill name"},
			"path": {"type": "string", "description": "File path relative to the skill's folder, such as checklist.md"}
		},
		"required": ["name", "path"],
		"additionalProperties": false
	}`)
}

func (t *ReadSkillFileTool) Risk() tools.Risk { return tools.RiskRead }

type readArgs struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (t *ReadSkillFileTool) Preview(args json.RawMessage) (string, error) {
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("%w: %v", tools.ErrValidation, err)
	}
	return fmt.Sprintf("Read %s from skill %s", a.Path, a.Name), nil
}

func (t *ReadSkillFileTool) Execute(ctx context.Context, args json.RawMessage) (tools.Result, error) {
	user, err := identity.UserFrom(ctx)
	if err != nil {
		return tools.Result{}, fmt.Errorf("read_skill_file: %w", err)
	}
	var a readArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tools.Result{}, fmt.Errorf("%w: %v", tools.ErrValidation, err)
	}
	l, err := t.catalog.lookup(user, a.Name)
	if err != nil {
		return tools.Result{}, err
	}
	// fs.ValidPath refuses absolute paths, ".." and "." elements and empty elements.
	if !fs.ValidPath(a.Path) || a.Path == "." || strings.Contains(a.Path, `\`) {
		return tools.Result{}, fmt.Errorf("invalid path %q", a.Path)
	}
	pinned, ok := l.files[a.Path]
	if !ok {
		return tools.Result{}, fmt.Errorf("unknown file %q", a.Path)
	}
	data, err := readConfined(l.Dir, a.Path)
	if err != nil {
		return tools.Result{}, err
	}
	if sha256.Sum256(data) != pinned {
		return tools.Result{}, fmt.Errorf("file %q changed since it was pinned", a.Path)
	}
	return tools.Result{Content: string(data)}, nil
}

// errNotRegular is returned when a path, or a folder on it, is a symbolic link or another
// non-regular file.
var errNotRegular = errors.New("not a regular file")

// readConfined reads dir/path through an os.Root, so it cannot leave dir, after checking that dir,
// every folder on the way and the file itself are real folders and a regular file, never links.
func readConfined(dir, path string) ([]byte, error) {
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("skill folder: %w", errNotRegular)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open skill folder: %w", err)
	}
	defer root.Close()

	parts := strings.Split(path, "/")
	for i := range parts {
		info, err := root.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			return nil, fmt.Errorf("file %q: %w", path, err)
		}
		last := i == len(parts)-1
		if (last && !info.Mode().IsRegular()) || (!last && !info.IsDir()) {
			return nil, fmt.Errorf("file %q: %w", path, errNotRegular)
		}
	}

	f, err := root.Open(path)
	if err != nil {
		return nil, fmt.Errorf("file %q: %w", path, err)
	}
	defer f.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, f); err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	return buf.Bytes(), nil
}
