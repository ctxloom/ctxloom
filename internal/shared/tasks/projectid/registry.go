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
	"slices"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	corepaths "github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/filelock"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// Entry is one row in the project registry: a stable project-id and the path
// it currently lives at.
type Entry struct {
	ProjectID  string    `yaml:"project_id"`
	Path       string    `yaml:"path"`
	CreatedAt  time.Time `yaml:"created_at"`
	LastSeenAt time.Time `yaml:"last_seen_at,omitempty"`
}

// registry is the on-disk form of the project registry.
type registry struct {
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

// Load reads the registry from disk. Returns an empty registry if the file
// doesn't exist (first-run case).
func (m *Manager) load() (*registry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

func (m *Manager) loadLocked() (*registry, error) {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return &registry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var reg registry
	if len(data) == 0 {
		return &reg, nil
	}
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return &reg, nil
}

func (m *Manager) saveLocked(reg *registry) error {
	data, err := yaml.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}
	return iox.WriteFileAtomic(m.path, data, 0o644)
}

// ResolveByPath returns a copy of the entry whose path matches projectDir, or
// nil if none.
func (m *Manager) ResolveByPath(projectDir string) (*Entry, error) {
	reg, err := m.load()
	if err != nil {
		return nil, err
	}
	want := cleanPath(projectDir)
	i := slices.IndexFunc(reg.Projects, func(e Entry) bool { return cleanPath(e.Path) == want })
	if i < 0 {
		return nil, nil
	}
	out := reg.Projects[i]
	return &out, nil
}

// EntriesAtPath returns every registry entry whose path matches projectDir
// (cleanPath-compared), regardless of project-id. Normally exactly one entry
// maps to a given path — ResolveByPath's "first match" is the common case —
// but a path can end up registered under more than one id (e.g. Adopt
// registering a marker's unknown id at a path the registry already had a
// DIFFERENT id for; Adopt never checks for or removes such a collision, only
// for a duplicate of its own id). Detecting a task-log/identity mismatch (see
// tasks/operations.missingLogSiblingNote) needs every such entry, not just
// whichever one ResolveByPath's fast path happens to prefer.
func (m *Manager) EntriesAtPath(projectDir string) ([]Entry, error) {
	reg, err := m.load()
	if err != nil {
		return nil, err
	}
	want := cleanPath(projectDir)
	var out []Entry
	for _, e := range reg.Projects {
		if cleanPath(e.Path) == want {
			out = append(out, e)
		}
	}
	return out, nil
}

// ResolveByID returns a copy of the entry with the given project-id, or nil.
func (m *Manager) ResolveByID(id string) (*Entry, error) {
	reg, err := m.load()
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(reg.Projects, func(e Entry) bool { return e.ProjectID == id })
	if i < 0 {
		return nil, nil
	}
	out := reg.Projects[i]
	return &out, nil
}

// mutate runs fn under both locks in the one safe order — in-process mutex,
// then file lock, then load — and fn may persist with saveLocked. Every
// mutating method goes through here, so a new one cannot reorder them or skip
// the file lock and corrupt the registry only under concurrency.
func (m *Manager) mutate(fn func(reg *registry) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	unlock, err := filelock.Lock(corepaths.PathFor(m.path))
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer unlock()

	reg, err := m.loadLocked()
	if err != nil {
		return err
	}
	return fn(reg)
}

// Mint generates a fresh project-id (collision-checked against the registry),
// appends an entry pointing at projectDir, and persists.
func (m *Manager) Mint(projectDir string) (Entry, error) {
	var out Entry
	err := m.mutate(func(reg *registry) error {
		// Re-check under the filelock that no entry already maps this exact
		// tree: Resolve decides "mint" with the registry unlocked, so two
		// processes first-launching the same brand-new tree can both reach
		// here. The first to win the lock appends; the second must return that
		// identity rather than append a second, competing entry — one tree
		// keeps one identity.
		want := cleanPath(projectDir)
		used := make(map[string]struct{}, len(reg.Projects))
		for _, p := range reg.Projects {
			used[p.ProjectID] = struct{}{}
			if cleanPath(p.Path) == want {
				out = p
				return nil
			}
		}
		id, err := generateUniqueID(used)
		if err != nil {
			return fmt.Errorf("mint project id: %w", err)
		}
		now := time.Now().UTC()
		e := Entry{
			ProjectID:  id,
			Path:       cleanPath(projectDir),
			CreatedAt:  now,
			LastSeenAt: now,
		}
		reg.Projects = append(reg.Projects, e)
		if err := m.saveLocked(reg); err != nil {
			return err
		}
		out = e
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return out, nil
}

// Adopt records an existing project-id at projectDir without minting — used
// when a marker references an id the local registry has not seen (a fresh
// machine, or a lost registry). Idempotent on the id.
func (m *Manager) Adopt(id, projectDir string) (Entry, error) {
	var out Entry
	err := m.mutate(func(reg *registry) error {
		now := time.Now().UTC()
		for i := range reg.Projects {
			if reg.Projects[i].ProjectID == id {
				reg.Projects[i].Path = cleanPath(projectDir)
				reg.Projects[i].LastSeenAt = now
				out = reg.Projects[i]
				return m.saveLocked(reg)
			}
		}
		e := Entry{ProjectID: id, Path: cleanPath(projectDir), CreatedAt: now, LastSeenAt: now}
		reg.Projects = append(reg.Projects, e)
		if err := m.saveLocked(reg); err != nil {
			return err
		}
		out = e
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return out, nil
}

// Repoint updates the registered path for an existing project-id.
func (m *Manager) Repoint(id, newPath string) error {
	return m.mutate(func(reg *registry) error {
		for i := range reg.Projects {
			if reg.Projects[i].ProjectID == id {
				reg.Projects[i].Path = cleanPath(newPath)
				reg.Projects[i].LastSeenAt = time.Now().UTC()
				return m.saveLocked(reg)
			}
		}
		return fmt.Errorf("project-id not found: %q", id)
	})
}

// cleanPath canonicalizes p for identity comparison: symlinks are resolved so
// the same tree reached via a symlink or alternate mount (/tmp vs /private/tmp
// on macOS) maps to ONE identity. Without this, a symlinked launch missed its
// registry entry, concluded "live copy", forked a fresh id, and overwrote the
// in-tree marker — orphaning the project's task log. Falls back to a lexical
// Clean when resolution fails (path gone, permission), so comparisons against
// dead registry entries still work.
func cleanPath(p string) string {
	if p == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// generateUniqueID picks a fresh project-id not already in `used`, mirroring
// the session harp allocator. Identity comes from the same harp generator;
// project-ids and session harps live in separate registries, so an incidental
// string match across the two is harmless.
func generateUniqueID(used map[string]struct{}) (string, error) {
	return harp.UniqueFrom(used, harp.GenerateName)
}
