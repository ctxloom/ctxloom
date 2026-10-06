//go:build unix

package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loosen opens dir to group and other, which on unix is the mode.
func loosen(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(dir, 0o755))
}

// A non-regular file at the lock path is refused even though it opens: only
// the type check stands between a FIFO and being "locked". The check is on
// what the path resolves to, so a link to a FIFO is refused the same way.
func TestNewLocks_RefuseFIFOLockPath(t *testing.T) {
	for name, open := range lockEntryPoints {
		t.Run(name, func(t *testing.T) {
			lockPath := filepath.Join(t.TempDir(), "x.lock")
			require.NoError(t, syscall.Mkfifo(lockPath, 0o600))
			require.ErrorIs(t, open(lockPath), ErrNotRegularFile)
		})
		t.Run(name+"ViaSymlink", func(t *testing.T) {
			dir := t.TempDir()
			fifo := filepath.Join(dir, "fifo")
			require.NoError(t, syscall.Mkfifo(fifo, 0o600))
			lockPath := filepath.Join(dir, "x.lock")
			require.NoError(t, os.Symlink(fifo, lockPath))
			require.ErrorIs(t, open(lockPath), ErrNotRegularFile)
		})
	}
}

// WithLock refuses BEFORE fn runs: a transaction under a lock that was never
// safely taken guarantees nothing.
func TestWithLock_RefusalNeverRunsFn(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	require.NoError(t, syscall.Mkfifo(lockPath, 0o600))
	ran := false
	err := WithLock(New().Locks, lockPath, func() error { ran = true; return nil })
	require.ErrorIs(t, err, ErrNotRegularFile)
	assert.False(t, ran)
}

// Held refuses a FIFO at the lock path rather than hanging on it.
func TestNewLocks_HeldRefusesFIFOLockPath(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	require.NoError(t, syscall.Mkfifo(lockPath, 0o600))
	_, err := New().Locks.Held(lockPath)
	require.ErrorIs(t, err, ErrNotRegularFile)
}

// On unix owner-only is the mode: Ensure tightens a dir that exists looser.
func TestNewPrivate_EnsureTightensALooseDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, New().Private.Ensure(dir))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, PrivateDirMode, info.Mode().Perm())
}

// Any group or other bit is exposure, refused as an *ExposedError naming the
// path and its mode.
func TestNewPrivate_CheckRefusesAnythingLooserThanOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		isDir bool
		mode  os.FileMode
	}{
		{"world-readable file", false, 0o644},
		{"group-readable file", false, 0o640},
		{"world-listable dir", true, 0o755},
		{"group-traversable dir", true, 0o710},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := New()
			dir := t.TempDir()
			require.NoError(t, root.Private.Ensure(dir))
			f := filepath.Join(dir, "secret")
			require.NoError(t, WriteFile(root.Fs, f, []byte("x"), PrivateFileMode))
			loose := f
			if tc.isDir {
				loose = dir
			}
			require.NoError(t, os.Chmod(loose, tc.mode))

			err := root.Private.Check(dir, f)
			var exposed *ExposedError
			require.True(t, errors.As(err, &exposed), "got %v", err)
			assert.Equal(t, loose, exposed.Path)
			assert.Contains(t, exposed.Why, "mode")
		})
	}
}
