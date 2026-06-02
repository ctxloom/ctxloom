package bundles

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// Source is the read port for bundles (ADR 0026): a place bundles load from.
// *Loader satisfies it; a database- or service-backed adapter would too.
type Source interface {
	Load(name string) (*Bundle, error)
	LoadFile(path string) (*Bundle, error)
}

// Store is the read+write port: a backing store bundles persist to. The
// filesystem adapter is the one returned by NewFSStore; MemStore is an
// in-memory adapter. Operations depends on this interface, never on a concrete
// store, so storage can change without touching core logic.
type Store interface {
	Source
	Save(b *Bundle) error
	Delete(name string) error
}

// fsStore is the filesystem Store adapter. It reads through an embedded Loader
// and writes/deletes through that Loader's afero.Fs, so reads and writes share
// one filesystem (the old Bundle.Save wrote via os while the Loader read via
// afero — a latent split this closes).
type fsStore struct {
	*Loader
}

// NewFSStore returns a filesystem-backed bundle Store over searchDirs.
func NewFSStore(searchDirs []string, preferDistilled bool, opts ...LoaderOption) Store {
	return &fsStore{Loader: NewLoader(searchDirs, preferDistilled, opts...)}
}

// Save writes the bundle back to its Path (which the caller sets — to the
// resolved path on load, or the target path on create), creating parent dirs.
func (s *fsStore) Save(b *Bundle) error {
	if b.Path == "" {
		return fmt.Errorf("bundle has no path set")
	}
	data, err := yaml.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal bundle: %w", err)
	}
	if err := s.fs.MkdirAll(filepath.Dir(b.Path), 0o755); err != nil {
		return fmt.Errorf("create bundle dir: %w", err)
	}
	if err := afero.WriteFile(s.fs, b.Path, data, 0o644); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	return nil
}

// Delete removes the file backing the named bundle.
func (s *fsStore) Delete(name string) error {
	path, err := s.Find(name)
	if err != nil {
		return err
	}
	return s.fs.Remove(path)
}

// MemStore is an in-memory Store adapter. It exists to demonstrate that
// operations are storage-agnostic (ADR 0026) and to give tests a
// filesystem-free backend. Keyed by name (a bundle's Path is set to its name on
// Seed/Save so a load-modify-save round-trips).
type MemStore struct {
	mu      sync.Mutex
	bundles map[string]*Bundle
}

// NewMemStore returns an empty in-memory bundle store.
func NewMemStore() *MemStore {
	return &MemStore{bundles: map[string]*Bundle{}}
}

// Seed inserts a bundle under name, setting its Path to name so a later Save
// round-trips to the same key.
func (m *MemStore) Seed(name string, b *Bundle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b.Path = name
	m.bundles[name] = b
}

func (m *MemStore) Load(name string) (*Bundle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.bundles[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, name)
	}
	return b, nil
}

func (m *MemStore) LoadFile(path string) (*Bundle, error) { return m.Load(path) }

func (m *MemStore) Save(b *Bundle) error {
	if b.Path == "" {
		return fmt.Errorf("bundle has no path/key set")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bundles[b.Path] = b
	return nil
}

func (m *MemStore) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bundles, name)
	return nil
}
