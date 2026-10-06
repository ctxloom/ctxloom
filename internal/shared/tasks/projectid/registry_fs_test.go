package projectid

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corepaths "github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The registry file is read and saved through the Manager's afero.Fs, so a
// decorator on that fs sees both. Driven on a MemMapFs at a registry path
// absent from disk: a raw os read would miss the saved entry, and a raw save
// would fail on the missing directory or land on disk.
func TestManager_SavesAndLoadsTheRegistryThroughItsFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	root := filepath.Join(t.TempDir(), "never-on-disk")
	path := filepath.Join(root, "projects", "index.yaml")
	require.NoError(t, mem.MkdirAll(filepath.Dir(path), 0o755))
	m := &Manager{path: path, root: safefs.NewMem(mem)}

	require.NoError(t, m.saveLocked(&registry{Projects: []Entry{{ProjectID: "high-tight-gulf", Path: "/work/p"}}}))

	got, err := m.ResolveByID("high-tight-gulf")
	require.NoError(t, err)
	require.NotNil(t, got, "the saved entry must be read back from the Manager's fs")
	assert.Equal(t, "/work/p", got.Path)

	_, err = os.Stat(root)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}

// A mutation holds the registry's lock on the Manager's Root for the whole
// load-modify-save — observed inside fn, where a TryLock that gives up at
// once is refused — and the lock, like the registry, stays on the Root's
// filesystem.
//
// MUTATION KILL: take the lock on the real OS (or not at all) and either
// the lock directory reaches disk or the in-fn TryLock succeeds.
func TestManager_MutatesUnderItsRootsLock(t *testing.T) {
	mem := afero.NewMemMapFs()
	root := filepath.Join(t.TempDir(), "never-on-disk")
	path := filepath.Join(root, "projects", "index.yaml")
	r := safefs.NewMem(mem)
	m := &Manager{path: path, root: r}

	gaveUp, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, m.mutate(func(*registry) error {
		_, err := r.Locks.TryLock(gaveUp, corepaths.PathFor(path))
		assert.ErrorIs(t, err, safefs.ErrLockHeld, "the registry lock is held across the mutation")
		return nil
	}))

	_, err := os.Stat(root)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}
