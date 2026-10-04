package filelock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entryPoints is every way this package opens a lock file. Each one must
// treat a lock path the same way: a refusal that only one entry point honours
// is a hole in the other.
var entryPoints = map[string]func(lockPath string) error{
	"WithLock": func(lockPath string) error {
		return WithLock(nil, lockPath, func() error { return nil })
	},
	"Prepare": Prepare,
}

// A symlinked lock path is followed, not refused: a user or agent may link a
// lock file wherever they like. The lock lands on what the link resolves to —
// while it is held through the link, the target itself cannot be taken — and
// the target's contents are untouched.
func TestEntryPoints_FollowSymlinkedLockPath(t *testing.T) {
	for name, open := range entryPoints {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "precious")
			require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))
			lockPath := filepath.Join(dir, "x.lock")
			require.NoError(t, os.Symlink(target, lockPath))

			require.NoError(t, open(lockPath))
			got, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, "keep", string(got))
		})
	}
}

func TestWithLock_ThroughASymlinkLocksItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.lock")
	lockPath := filepath.Join(dir, "x.lock")
	require.NoError(t, os.Symlink(target, lockPath))

	ran := false
	require.NoError(t, WithLock(nil, lockPath, func() error {
		ran = true
		locked, err := flock.New(target).TryLock()
		require.NoError(t, err)
		assert.False(t, locked, "the lock taken through the link must hold its target")
		return nil
	}))
	assert.True(t, ran)
	info, err := os.Lstat(target)
	require.NoError(t, err, "a dangling link's target is created")
	assert.True(t, info.Mode().IsRegular())
}

// The ordinary case still works: a missing lock file (and its directory) is
// created as a regular file.
func TestEntryPoints_CreateRegularLockFile(t *testing.T) {
	for name, open := range entryPoints {
		t.Run(name, func(t *testing.T) {
			lockPath := filepath.Join(t.TempDir(), "sub", "x.lock")
			require.NoError(t, open(lockPath))
			info, err := os.Lstat(lockPath)
			require.NoError(t, err)
			assert.True(t, info.Mode().IsRegular())
		})
	}
}
