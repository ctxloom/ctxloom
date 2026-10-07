package safefs

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// existingRoots are both Locks implementations with a directory that exists
// and a lock path in it, and a way to see whether that path exists.
func existingRoots(t *testing.T) map[string]struct {
	locks  Locks
	path   string
	exists func() bool
} {
	dir := t.TempDir()
	mem := afero.NewMemMapFs()
	require.NoError(t, mem.MkdirAll("/l", 0o755))
	exists := func(fsys afero.Fs, p string) func() bool {
		return func() bool {
			ok, err := afero.Exists(fsys, p)
			require.NoError(t, err)
			return ok
		}
	}
	osPath := filepath.Join(dir, "x.lock")
	return map[string]struct {
		locks  Locks
		path   string
		exists func() bool
	}{
		"os":  {New().Locks, osPath, exists(afero.NewOsFs(), osPath)},
		"mem": {NewMem(mem).Locks, "/l/x.lock", exists(mem, "/l/x.lock")},
	}
}

// TryLockExisting on a missing lock file is fs.ErrNotExist and leaves the
// path as it found it: a prober that must not leave a lock file behind has
// nothing to clean up. With a done context it is still the missing file —
// never ErrLockHeld, which would read the absence as a live holder.
func TestLocks_TryLockExistingCreatesNothing(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			_, err := r.locks.TryLockExisting(expired(), r.path)
			require.ErrorIs(t, err, fs.ErrNotExist)
			require.NotErrorIs(t, err, ErrLockHeld)
			assert.False(t, r.exists(), "the probe created the lock file")
		})
	}
}

// TryLockExisting takes an existing lock file exactly as TryLock does: held
// against every other taker until released, and refused while another holds
// it.
func TestLocks_TryLockExistingTakesAnExistingFile(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			first, err := r.locks.Lock(r.path)
			require.NoError(t, err)
			_, err = r.locks.TryLockExisting(expired(), r.path)
			require.ErrorIs(t, err, ErrLockHeld, "another holder's lock was taken")
			require.NoError(t, first.Unlock())

			lk, err := r.locks.TryLockExisting(expired(), r.path)
			require.NoError(t, err)
			assert.True(t, lk.Current())
			_, err = r.locks.TryLock(expired(), r.path)
			require.ErrorIs(t, err, ErrLockHeld, "the probe's lock excludes a taker")
			require.NoError(t, lk.Unlock())
		})
	}
}

// TryLockExisting refuses a lock path that is not a regular file.
func TestLocks_TryLockExistingRefusesANonRegularFile(t *testing.T) {
	for name, r := range existingRoots(t) {
		t.Run(name, func(t *testing.T) {
			dirAt := filepath.Join(filepath.Dir(r.path), "a-dir")
			inner, err := r.locks.Lock(filepath.Join(dirAt, "inner.lock"))
			require.NoError(t, err)
			defer func() { _ = inner.Unlock() }()
			_, err = r.locks.TryLockExisting(expired(), dirAt)
			require.ErrorIs(t, err, ErrNotRegularFile)
		})
	}
}
