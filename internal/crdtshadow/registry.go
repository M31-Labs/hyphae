package crdtshadow

import (
	"sync"
)

// Registry caches one Shadow per space root. Concurrent-safe. Most code
// reaches through the Default package-level registry; tests can build
// their own with NewRegistry.
type Registry struct {
	mu      sync.Mutex
	shadows map[string]*Shadow
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{shadows: make(map[string]*Shadow)}
}

// Get returns the cached Shadow for spaceRoot, opening it if absent.
// Multiple callers with the same spaceRoot share the same Shadow.
func (r *Registry) Get(spaceRoot, spaceID string) (*Shadow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.shadows[spaceRoot]; ok {
		return s, nil
	}
	s, err := Open(spaceRoot, spaceID)
	if err != nil {
		return nil, err
	}
	r.shadows[spaceRoot] = s
	return s, nil
}

// Close persists every cached Shadow. Safe to call multiple times.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for _, s := range r.shadows {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Default is the package-level registry used by CLI and MCP
// dispatchers. Tests should build their own with NewRegistry to avoid
// global state leaks.
var Default = NewRegistry()
