//go:build unix

package filelock

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// A non-regular file planted at the lock path is refused even though it opens:
// a FIFO opened O_RDWR succeeds on Linux, so only the post-open type check
// stands between it and being "locked".
func TestEntryPoints_RefuseFIFOLockPath(t *testing.T) {
	for name, open := range entryPoints {
		t.Run(name, func(t *testing.T) {
			lockPath := filepath.Join(t.TempDir(), "x.lock")
			require.NoError(t, syscall.Mkfifo(lockPath, 0o600))
			require.ErrorIs(t, open(lockPath), ErrNotRegularFile)
		})
	}
}
