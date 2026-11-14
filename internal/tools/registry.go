package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

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

// Validate checks that args match the registered tool's JSON Schema.
func (r *Registry) Validate(name string, args json.RawMessage) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	_, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	sch := r.schemas[name]
	if sch == nil {
		return nil
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	val, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrValidation, err)
	}
	if err := sch.Validate(val); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

// Validate checks that args match a tool's JSON Schema.
func Validate(t Tool, args json.RawMessage) error {
	rawSchema := t.Schema()
	if len(bytes.TrimSpace(rawSchema)) == 0 {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return fmt.Errorf("invalid tool schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	url := fmt.Sprintf("schema://%s.json", t.Name())
	if err := c.AddResource(url, doc); err != nil {
		return fmt.Errorf("add schema resource: %w", err)
	}
	sch, err := c.Compile(url)
	if err != nil {
		return fmt.Errorf("compile tool schema: %w", err)
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage("{}")
	}
	val, err := jsonschema.UnmarshalJSON(bytes.NewReader(args))
	if err != nil {
		return fmt.Errorf("%w: invalid JSON: %v", ErrValidation, err)
	}
	if err := sch.Validate(val); err != nil {
		return fmt.Errorf("%w: %v", ErrValidation, err)
	}
	return nil
}

// AgentView wraps a Registry to enforce that critical tools cannot be executed by the agent.
type AgentView struct {
	reg *Registry
}

// AgentView returns a view of the registry that refuses direct execution of critical tools.
func (r *Registry) AgentView() *AgentView {
	return &AgentView{reg: r}
}

func (v *AgentView) Tools() []model.Tool {
	return v.reg.Tools()
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

func (r *refusingTool) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	return Result{}, ErrCriticalRefusal
}
