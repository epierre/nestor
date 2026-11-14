package tools

import (
	"errors"
	"fmt"
	"sync"

	"github.com/epierre/nestor/internal/model"
)

var (
	// ErrNotFound is returned when a tool is not registered.
	ErrNotFound = errors.New("tool not found")
	// ErrAlreadyExists is returned when registering a tool with a name that is already taken.
	ErrAlreadyExists = errors.New("tool already registered")
)

// Registry is an in-memory collection of tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. It returns an error if a tool with the same name exists.
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
	r.tools[name] = t
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
