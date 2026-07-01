package profiles

import (
	"fmt"
	"sort"
	"sync"
)

// MemStore is an in-memory profiles Store adapter. It exists to prove the
// operations layer is storage-agnostic (ADR 0026) and to give tests a
// filesystem-free backend. Keyed by profile Name.
type MemStore struct {
	mu       sync.Mutex
	profiles map[string]*Profile
}

// NewMemStore returns an empty in-memory profile store.
func NewMemStore() *MemStore {
	return &MemStore{profiles: map[string]*Profile{}}
}

// Seed inserts (or replaces) a profile under its Name without going through
// Save's bookkeeping — a test convenience for arranging fixtures.
func (m *MemStore) Seed(profiles ...*Profile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range profiles {
		m.profiles[p.Name] = p
	}
}

// List returns every stored profile, name-sorted for deterministic output.
func (m *MemStore) List() ([]*Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Profile, 0, len(m.profiles))
	for _, p := range m.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load returns the named profile or an error if absent.
func (m *MemStore) Load(name string) (*Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.profiles[name]
	if !ok {
		return nil, fmt.Errorf("profile %q not found", name)
	}
	return p, nil
}

// Exists reports whether the named profile is stored.
func (m *MemStore) Exists(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.profiles[name]
	return ok
}

// Save persists the profile under its Name (create or overwrite).
func (m *MemStore) Save(p *Profile) error {
	if p == nil || p.Name == "" {
		return fmt.Errorf("profile has no name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profiles[p.Name] = p
	return nil
}

// Delete removes the named profile, erroring if it is not present.
func (m *MemStore) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.profiles[name]; !ok {
		return fmt.Errorf("profile %q not found", name)
	}
	delete(m.profiles, name)
	return nil
}
