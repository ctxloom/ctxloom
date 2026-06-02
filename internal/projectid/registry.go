// Package projectid resolves a stable, path-independent identity for a project
// and maintains the home-rooted registry that maps each project-id to its
// current path. Identity is carried in two places that heal each other: a
// gitignored in-tree marker (<projectDir>/.ctxloom/project-id) that travels
// with the working tree, and the registry (~/.ctxloom/projects/index.yaml)
// that the rest of ctxloom keys task logs on. See ADR 0025.
package projectid

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/filelock"
	"github.com/ctxloom/ctxloom/internal/harp"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// Entry is one row in the project registry: a stable project-id and the path
// it currently lives at.
type Entry struct {
	ProjectID  string    `yaml:"project_id"`
	Path       string    `yaml:"path"`
	CreatedAt  time.Time `yaml:"created_at"`
	LastSeenAt time.Time `yaml:"last_seen_at,omitempty"`
}

// Registry is the on-disk form of the project registry.
type Registry struct {
	Projects []Entry `yaml:"projects"`
}

// Manager owns load/save of a single registry file with a cooperative lock,
// mirroring sessions.Manager. All mutations go through its methods so the file
// lock and in-memory state stay consistent.
type Manager struct {
	path string
	mu   sync.Mutex
}

// Open returns a Manager for the home-rooted registry at
// ~/.ctxloom/projects/index.yaml unless override is non-empty.
func Open(override string) (*Manager, error) {
	path := override
	if path == "" {
		p, err := paths.ProjectRegistryPath()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir projects dir: %w", err)
	}
	return &Manager{path: path}, nil
}

// Path returns the absolute path to the registry file.
func (m *Manager) Path() string { return m.path }

// Load reads the registry from disk. Returns an empty Registry if the file
// doesn't exist (first-run case).
func (m *Manager) Load() (*Registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) loadLocked() (*Registry, error) {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return &Registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var reg Registry
	if len(data) == 0 {
		return &reg, nil
	}
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return &reg, nil
}

func (m *Manager) saveLocked(reg *Registry) error {
	data, err := yaml.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// ResolveByPath returns a copy of the entry whose path matches projectDir, or
// nil if none.
func (m *Manager) ResolveByPath(projectDir string) (*Entry, error) {
	reg, err := m.Load()
	if err != nil {
		return nil, err
	}
	want := cleanPath(projectDir)
	for i := range reg.Projects {
		if cleanPath(reg.Projects[i].Path) == want {
			out := reg.Projects[i]
			return &out, nil
		}
	}
	return nil, nil
}

// ResolveByID returns a copy of the entry with the given project-id, or nil.
func (m *Manager) ResolveByID(id string) (*Entry, error) {
	reg, err := m.Load()
	if err != nil {
		return nil, err
	}
	for i := range reg.Projects {
		if reg.Projects[i].ProjectID == id {
			out := reg.Projects[i]
			return &out, nil
		}
	}
	return nil, nil
}

// Mint generates a fresh project-id (collision-checked against the registry),
// appends an entry pointing at projectDir, and persists.
func (m *Manager) Mint(projectDir string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return Entry{}, fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	reg, err := m.loadLocked()
	if err != nil {
		return Entry{}, err
	}
	used := make(map[string]struct{}, len(reg.Projects))
	for _, p := range reg.Projects {
		used[p.ProjectID] = struct{}{}
	}
	now := time.Now().UTC()
	e := Entry{
		ProjectID:  generateUniqueID(used),
		Path:       cleanPath(projectDir),
		CreatedAt:  now,
		LastSeenAt: now,
	}
	reg.Projects = append(reg.Projects, e)
	if err := m.saveLocked(reg); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Adopt records an existing project-id at projectDir without minting — used
// when a marker references an id the local registry has not seen (a fresh
// machine, or a lost registry). Idempotent on the id.
func (m *Manager) Adopt(id, projectDir string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return Entry{}, fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	reg, err := m.loadLocked()
	if err != nil {
		return Entry{}, err
	}
	now := time.Now().UTC()
	for i := range reg.Projects {
		if reg.Projects[i].ProjectID == id {
			reg.Projects[i].Path = cleanPath(projectDir)
			reg.Projects[i].LastSeenAt = now
			out := reg.Projects[i]
			return out, m.saveLocked(reg)
		}
	}
	e := Entry{ProjectID: id, Path: cleanPath(projectDir), CreatedAt: now, LastSeenAt: now}
	reg.Projects = append(reg.Projects, e)
	if err := m.saveLocked(reg); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// Repoint updates the registered path for an existing project-id.
func (m *Manager) Repoint(id, newPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(m.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	reg, err := m.loadLocked()
	if err != nil {
		return err
	}
	for i := range reg.Projects {
		if reg.Projects[i].ProjectID == id {
			reg.Projects[i].Path = cleanPath(newPath)
			reg.Projects[i].LastSeenAt = time.Now().UTC()
			return m.saveLocked(reg)
		}
	}
	return fmt.Errorf("project-id not found: %q", id)
}

func cleanPath(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

// generateUniqueID picks a fresh project-id not already in `used`, mirroring
// the session harp allocator. Identity comes from the same harp generator;
// project-ids and session harps live in separate registries, so an incidental
// string match across the two is harmless.
func generateUniqueID(used map[string]struct{}) string {
	for range 100 {
		id := harp.GenerateName()
		if _, dup := used[id]; !dup {
			return id
		}
	}
	return harp.GenerateName()
}
