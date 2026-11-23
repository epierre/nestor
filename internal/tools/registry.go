package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/epierre/nestor/internal/identity"
	"github.com/epierre/nestor/internal/model"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	// ErrNotFound is returned when a tool is not registered.
	ErrNotFound = errors.New("tool not found")
	// ErrAlreadyExists is returned when registering a tool with a name that is already taken.
	ErrAlreadyExists = errors.New("tool already registered")
	// ErrValidation is returned when arguments fail JSON Schema validation.
	ErrValidation = errors.New("argument validation failed")
	// ErrCriticalRefusal is returned when attempting to execute a critical tool through an AgentView.
	ErrCriticalRefusal = errors.New("execution of critical tools is forbidden without approval")
)

// Registry is an in-memory collection of tools.
type Registry struct {
	mu      sync.RWMutex
	tools   map[string]Tool
	schemas map[string]*jsonschema.Schema
	order   []string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools:   make(map[string]Tool),
		schemas: make(map[string]*jsonschema.Schema),
	}
}

// Register adds a tool to the registry. It validates and compiles the tool's schema.
// It returns an error if a tool with the same name exists or if the schema is invalid.
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	if name == "" {
		return errors.New("tool name cannot be empty")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("%w: %q", ErrAlreadyExists, name)
	}

	var sch *jsonschema.Schema
	rawSchema := t.Schema()
	if len(bytes.TrimSpace(rawSchema)) > 0 {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
		if err != nil {
			return fmt.Errorf("invalid tool schema: %w", err)
		}
		c := jsonschema.NewCompiler()
		url := fmt.Sprintf("schema://%s.json", name)
		if err := c.AddResource(url, doc); err != nil {
			return fmt.Errorf("add schema resource: %w", err)
		}
		compiled, err := c.Compile(url)
		if err != nil {
			return fmt.Errorf("compile tool schema: %w", err)
		}
		sch = compiled
	}

	r.tools[name] = t
	r.schemas[name] = sch
	r.order = append(r.order, name)
	return nil
}

// Lookup finds a tool by name, or returns ErrNotFound if unknown.
func (r *Registry) Lookup(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return t, nil
}

// Tools returns the list of registered tools formatted for language model requests,
// in registration order.
// Unregister removes the tool registered under name, such as an MCP tool whose server became
// unavailable. It reports whether there was one.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	delete(r.schemas, name)
	r.order = slices.DeleteFunc(r.order, func(n string) bool { return n == name })
	return true
}

// ToolsFor lists the tools that user may use, in registration order.
func (r *Registry) ToolsFor(ctx context.Context, user identity.UserID) []model.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]model.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		if !Enabled(ctx, t, user) {
			continue
		}
		out = append(out, model.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return out
}

func (r *Registry) Tools() []model.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]model.Tool, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, model.Tool{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.Schema(),
		})
	}
	return out
}

// Validate checks that args are a strict JSON object with no duplicate keys or trailing tokens,
// and match the registered tool's JSON Schema when one is compiled.
func (r *Registry) Validate(name string, args json.RawMessage) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	if err := validateStrictJSONObject(args); err != nil {
		return err
	}
	sch := r.schemas[name]
	if sch == nil {
		return nil
	}
	val, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
	if err := sch.Validate(val); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

// validateStrictJSONObject enforces a single JSON object root with no duplicate keys at any depth
// and no trailing tokens, returning ErrValidation without echoing untrusted argument content.
func validateStrictJSONObject(raw json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("%w: arguments must be a JSON object", ErrValidation)
	}
	if err := validateJSONObjectBody(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: trailing JSON tokens", ErrValidation)
		}
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
	return nil
}

func validateJSONObjectBody(dec *json.Decoder) error {
	seen := make(map[string]struct{})
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: invalid JSON", ErrValidation)
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("%w: invalid JSON", ErrValidation)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: duplicate JSON key", ErrValidation)
		}
		seen[key] = struct{}{}
		if err := validateJSONValue(dec); err != nil {
			return err
		}
	}
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '}' {
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
	return nil
}

func validateJSONValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return validateJSONObjectBody(dec)
	case '[':
		for dec.More() {
			if err := validateJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: invalid JSON", ErrValidation)
		}
		if endDelim, ok := end.(json.Delim); !ok || endDelim != ']' {
			return fmt.Errorf("%w: invalid JSON", ErrValidation)
		}
		return nil
	default:
		return fmt.Errorf("%w: invalid JSON", ErrValidation)
	}
}

// AgentView wraps a Registry to enforce that critical tools cannot be executed by the agent.
type AgentView struct {
	reg *Registry
}

// AgentView returns a view of the registry that refuses direct execution of critical tools.
func (r *Registry) AgentView() *AgentView {
	return &AgentView{reg: r}
}

// ToolsFor lists the tools offered to user in a model request.
func (v *AgentView) ToolsFor(ctx context.Context, user identity.UserID) []model.Tool {
	return v.reg.ToolsFor(ctx, user)
}

func (v *AgentView) Lookup(name string) (Tool, error) {
	t, err := v.reg.Lookup(name)
	if err != nil {
		return nil, err
	}
	if t.Risk() == RiskCritical {
		return &refusingTool{Tool: t}, nil
	}
	return t, nil
}

func (v *AgentView) Validate(name string, args json.RawMessage) error {
	return v.reg.Validate(name, args)
}

type refusingTool struct {
	Tool
}

// EnabledFor keeps the wrapped tool's user scope. The embedded interface would otherwise hide it.
func (r refusingTool) EnabledFor(ctx context.Context, user identity.UserID) bool {
	return Enabled(ctx, r.Tool, user)
}

func (r *refusingTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	return Result{}, ErrCriticalRefusal
}

// PreviewFor forwards to the wrapped tool, so that the wrapper previews as the tool does.
func (r *refusingTool) PreviewFor(ctx context.Context, args json.RawMessage) (string, error) {
	if cp, ok := r.Tool.(ContextPreviewer); ok {
		return cp.PreviewFor(ctx, args)
	}
	return r.Tool.Preview(args)
}
