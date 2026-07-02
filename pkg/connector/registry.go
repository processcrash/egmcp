package connector

import (
	"fmt"
	"sort"
	"sync"
)

// Factory is a constructor for a Connector given the user-supplied
// config blob. The returned Connector must not have side effects;
// Init() is called separately by the platform.
type Factory func() Connector

// Registry tracks Connectors by their Manifest.Name. Both built-in
// and dynamically loaded plugins register here on platform startup.
type Registry struct {
	mu    sync.RWMutex
	items map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Factory)}
}

// Register adds a Connector factory. Calling Register twice with the
// same name panics; the platform considers name collisions a bug.
func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[name]; exists {
		panic(fmt.Sprintf("connector %q already registered", name))
	}
	r.items[name] = factory
}

// MustRegister is a tiny wrapper that panics on error for init-time use.
func (r *Registry) MustRegister(name string, factory Factory) {
	r.Register(name, factory)
}

// Get returns the factory for name, or nil and false if unknown.
func (r *Registry) Get(name string) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.items[name]
	return f, ok
}

// Names lists registered connector names in sorted order. Used by the
// /api/v1/connectors/builtin endpoint.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.items))
	for n := range r.items {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
