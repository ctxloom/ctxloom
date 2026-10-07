//go:build windows

package safefs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Windows byte-range locks are mandatory, so New's locks hold a byte past any
// content: a held lock leaves the file readable through every other handle
// (a session lock's pid stays visible), another taker of the same lock is
// still refused, and the file can still be removed while it is held (a Hold
// that loses removes it under the winner).
func TestNewLocks_AHeldLockLeavesTheContentReadableAndTheFileRemovable(t *testing.T) {
	for name, take := range map[string]func(Locks, string) (Lock, error){
		"Lock":  func(l Locks, p string) (Lock, error) { return l.Lock(p) },
		"RLock": func(l Locks, p string) (Lock, error) { return l.RLock(p) },
		"TryLock": func(l Locks, p string) (Lock, error) {
			return l.TryLock(expired(), p)
		},
	} {
		t.Run(name, func(t *testing.T) {
			lockPath := filepath.Join(t.TempDir(), "harp.lock")
			require.NoError(t, WriteFileInPlace(lockPath, TruncateInPlace, []byte("4242\n"), 0o600))
			locks := New().Locks
			lk, err := take(locks, lockPath)
			require.NoError(t, err)
			defer func() { _ = lk.Unlock() }()

			got, err := os.ReadFile(lockPath)
			require.NoError(t, err, "a held lock must not make the content unreadable")
			assert.Equal(t, "4242\n", string(got))

			_, err = locks.TryLock(expired(), lockPath)
			require.ErrorIs(t, err, ErrLockHeld, "the lock must still exclude another taker")
			held, err := locks.Held(lockPath)
			require.NoError(t, err)
			assert.True(t, held)

			require.NoError(t, os.Remove(lockPath), "a held lock file must stay removable")
		})
	}
}
