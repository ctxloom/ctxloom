package safefs

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockRoots are both Locks implementations, each with a lock path it can take.
func lockRoots(t *testing.T) map[string]struct {
	locks Locks
	path  string
} {
	return map[string]struct {
		locks Locks
		path  string
	}{
		"os":  {New().Locks, filepath.Join(t.TempDir(), "x.lock")},
		"mem": {NewMem(afero.NewMemMapFs()).Locks, "/l/x.lock"},
	}
}

// A shared hold excludes a writer — TryLock's one attempt finds it held, and
// Held reports it — and a writer gets in once the reader releases.
func TestLocks_RLockExcludesAWriter(t *testing.T) {
	for name, r := range lockRoots(t) {
		t.Run(name, func(t *testing.T) {
			rl, err := r.locks.RLock(r.path)
			require.NoError(t, err)
			_, err = r.locks.TryLock(expired(), r.path)
			require.ErrorIs(t, err, ErrLockHeld, "a writer was let in beside a reader")
			held, err := r.locks.Held(r.path)
			require.NoError(t, err)
			assert.True(t, held, "a shared hold is a hold")
			require.NoError(t, rl.Unlock())
			w, err := r.locks.TryLock(expired(), r.path)
			require.NoError(t, err, "the reader's release lets a writer in")
			require.NoError(t, w.Unlock())
		})
	}
}

// RLock creates a missing lock file and its directory, as Lock does.
func TestLocks_RLockCreatesTheLockFile(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	l, err := root.Locks.RLock("/new/dir/x.lock")
	require.NoError(t, err)
	require.NoError(t, l.Unlock())
	exists, err := afero.Exists(root.Fs, "/new/dir/x.lock")
	require.NoError(t, err)
	assert.True(t, exists)
}

// A writer waiting on two readers is granted once both have released.
func TestLocks_AWriterProceedsWhenEveryReaderReleases(t *testing.T) {
	for name, r := range lockRoots(t) {
		t.Run(name, func(t *testing.T) {
			r1, err := r.locks.RLock(r.path)
			require.NoError(t, err)
			r2, err := r.locks.RLock(r.path)
			require.NoError(t, err, "a second reader is admitted beside the first")
			got := make(chan error)
			go func() {
				w, err := r.locks.Lock(r.path)
				if err == nil {
					err = w.Unlock()
				}
				got <- err
			}()
			require.NoError(t, r1.Unlock())
			require.NoError(t, r2.Unlock())
			require.NoError(t, <-got)
		})
	}
}

// The in-memory lock admits readers together and keeps them out while a
// writer holds it, probed with a single attempt each way so nothing waits.
func TestNewMem_ReadersShareAndAWriterExcludesThem(t *testing.T) {
	l := memLocks{fs: afero.NewMemMapFs()}
	rw := l.slot("/l/x.lock")

	require.True(t, rw.acquire(expired(), true))
	assert.True(t, rw.acquire(expired(), true), "a second reader must be admitted beside the first")
	assert.False(t, rw.acquire(expired(), false), "a writer must wait for the readers")
	rw.release(true)
	rw.release(true)

	require.True(t, rw.acquire(expired(), false))
	assert.False(t, rw.acquire(expired(), true), "a reader must wait for the writer")
	rw.release(false)
	assert.True(t, rw.acquire(expired(), true), "the writer's release admits a reader")
	rw.release(true)
}
