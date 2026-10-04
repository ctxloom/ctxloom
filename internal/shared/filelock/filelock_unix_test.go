//go:build unix

package filelock

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A non-regular file at the lock path is refused even though it opens: a FIFO
// opened O_RDWR succeeds on Linux, so only the type check stands between it
// and being "locked". The check is on what the path resolves to, so a link to
// a FIFO is refused the same way.
func TestEntryPoints_RefuseFIFOLockPath(t *testing.T) {
	for name, open := range entryPoints {
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
	err := WithLock(nil, lockPath, func() error { ran = true; return nil })
	require.ErrorIs(t, err, ErrNotRegularFile)
	assert.False(t, ran)
}

// Held refuses a FIFO at the lock path rather than hanging on it: a
// read-only open of a FIFO blocks until a writer appears, and a liveness
// probe that hangs is worse than one that errs.
func TestHeld_RefusesFIFOLockPath(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	require.NoError(t, syscall.Mkfifo(lockPath, 0o600))
	_, err := Held(lockPath)
	require.ErrorIs(t, err, ErrNotRegularFile)
}
