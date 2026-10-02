package filelock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entryPoints is every way this package opens a lock file. Each one must
// refuse the same unsafe lock paths: a lock path is attacker-plantable
// wherever another principal can write the lock directory, so a refusal that
// only one entry point honours is a hole in the other.
var entryPoints = map[string]func(lockPath string) error{
	"WithLock": func(lockPath string) error {
		return WithLock(nil, lockPath, func() error { return nil })
	},
	"Prepare": Prepare,
}

// A symlink planted at the lock path is refused, never followed: following it
// would create (O_CREATE) an empty file wherever the link points, anywhere the
// invoking user can write.
func TestEntryPoints_RefuseSymlinkedLockPath(t *testing.T) {
	for name, open := range entryPoints {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "elsewhere")
			lockPath := filepath.Join(dir, "x.lock")
			require.NoError(t, os.Symlink(target, lockPath))

			err := open(lockPath)
			require.ErrorIs(t, err, ErrNotRegularFile)
			_, statErr := os.Lstat(target)
			assert.True(t, os.IsNotExist(statErr), "the symlink's target must not be created")
		})
	}
}

// A symlink to an EXISTING file is refused too: following it would hand the
// lock (and, through O_RDWR, write access) to whatever file it names.
func TestEntryPoints_RefuseSymlinkToExistingFile(t *testing.T) {
	for name, open := range entryPoints {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "precious")
			require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))
			lockPath := filepath.Join(dir, "x.lock")
			require.NoError(t, os.Symlink(target, lockPath))

			require.ErrorIs(t, open(lockPath), ErrNotRegularFile)
			got, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, "keep", string(got))
		})
	}
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

// WithLock refuses BEFORE fn runs: a transaction under a lock that was never
// safely taken guarantees nothing.
func TestWithLock_RefusalNeverRunsFn(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "x.lock")
	require.NoError(t, os.Symlink(filepath.Join(dir, "elsewhere"), lockPath))
	ran := false
	err := WithLock(nil, lockPath, func() error { ran = true; return nil })
	require.ErrorIs(t, err, ErrNotRegularFile)
	assert.False(t, ran)
}
