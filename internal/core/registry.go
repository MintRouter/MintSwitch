package core

import "sync"

// Registry is a concurrency-safe collection of tool adapters keyed by ID.
//
// Registration order is preserved so the UI can present tools deterministically.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]ToolAdapter
	order    []string
}

// NewRegistry returns an empty Registry ready for use.
func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]ToolAdapter)}
}

// Register adds (or replaces) an adapter. A nil adapter or one with an empty ID
// is ignored. Re-registering an existing ID replaces it without changing order.
func (r *Registry) Register(a ToolAdapter) {
	if a == nil || a.ID() == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := a.ID()
	if _, exists := r.adapters[id]; !exists {
		r.order = append(r.order, id)
	}
	r.adapters[id] = a
}

// Unregister removes the adapter with the given ID, preserving the registration
// order of the remaining adapters. It is a no-op when id is not registered.
func (r *Registry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[id]; !exists {
		return
	}
	delete(r.adapters, id)
	for i, oid := range r.order {
		if oid == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// All returns the registered adapters in registration order.
func (r *Registry) All() []ToolAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolAdapter, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.adapters[id])
	}
	return out
}

// Get returns the adapter for id and whether it was found.
func (r *Registry) Get(id string) (ToolAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[id]
	return a, ok
}
