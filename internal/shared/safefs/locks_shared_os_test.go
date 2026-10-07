//go:build !windows

package safefs

import (
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

// New's RLock is the kernel's shared lock: another process's shared attempt
// is granted beside it and its exclusive attempt is refused. gofrs/flock is
// that kernel lock here; on Windows the lock byte is safefs's own
// (locks_kernel_windows.go), so a raw gofrs handle would not contend.
func TestNewLocks_RLockIsTheKernelsSharedLock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	rl, err := New().Locks.RLock(lockPath)
	require.NoError(t, err)
	defer func() { _ = rl.Unlock() }()

	other := flock.New(lockPath)
	defer func() { _ = other.Close() }()
	got, err := other.TryLock()
	require.NoError(t, err)
	require.False(t, got, "an exclusive taker was let in beside a shared hold")
	got, err = other.TryRLock()
	require.NoError(t, err)
	require.True(t, got, "a shared taker must be admitted beside a shared hold")
}
