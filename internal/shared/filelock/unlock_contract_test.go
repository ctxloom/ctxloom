package filelock_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// blockedLockPath returns a path whose parent directory cannot be created,
// because an ordinary FILE sits where a directory would have to go. That makes
// the acquisition fail inside ensureDir, before any file is opened.
func blockedLockPath(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))
	// The fixture is only hostile if the blocker really does defeat MkdirAll.
	require.Error(t, os.MkdirAll(filepath.Join(blocker, "nested"), 0o755),
		"fixture is not hostile: the parent directory was creatable after all")
	return filepath.Join(blocker, "nested", "guard.lock")
}

// The release function must be safe to call unconditionally. `defer unlock()`
// placed immediately after the call — the shape every caller in this repo
// uses, and the shape the package's own doc comment shows — runs on the error
// path too, so a nil release turns a lock that could not be taken into a
// panic in the caller. The failure to acquire is the caller's business; a
// crash is not.
func TestLock_FailureStillReturnsACallableRelease(t *testing.T) {
	for name, acquire := range map[string]func(string) (func(), error){
		"Lock":       filelock.Lock,
		"LockShared": filelock.LockShared,
	} {
		t.Run(name, func(t *testing.T) {
			unlock, err := acquire(blockedLockPath(t))
			require.Error(t, err, "the fixture must make acquisition fail")
			require.NotNil(t, unlock, "a failed acquisition must still return a callable release")
			unlock()
			unlock() // and it must stay callable: releasing nothing, twice, is not an error
		})
	}
}

// The caller shape the package documents, written out in full: acquire, defer
// the release, then handle the error. This is the one that panicked.
func TestLock_DeferredReleaseBeforeErrorCheckDoesNotPanic(t *testing.T) {
	func() {
		unlock, err := filelock.Lock(blockedLockPath(t))
		defer unlock()
		require.Error(t, err)
	}()
}
