package filelock_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// A caller of this package is, by construction, also reading, parsing and
// writing the file the lock protects. A bare "mkdir /home/u/.ctxloom:
// read-only file system" or "bad file descriptor" arriving there is
// indistinguishable from a failure of that work — and the two want opposite
// responses: a lock failure means someone else may be writing, a write failure
// means the caller's own data did not land. Every error must therefore name
// itself as a lock failure and name the lock.
func TestLockErrors_NameThemselvesAndTheLock(t *testing.T) {
	for name, acquire := range map[string]func(string) (func(), error){
		"Lock":       filelock.Lock,
		"LockShared": filelock.LockShared,
	} {
		t.Run(name, func(t *testing.T) {
			path := blockedLockPath(t)
			_, err := acquire(path)
			require.Error(t, err)

			msg := err.Error()
			if !strings.Contains(msg, "filelock:") {
				t.Errorf("error does not identify itself as a lock failure: %q", msg)
			}
			if !strings.Contains(msg, path) {
				t.Errorf("error does not name the lock it failed on (%s): %q", path, msg)
			}
		})
	}
}

// Wrapping must not hide the cause: a caller deciding whether to retry needs
// errors.Is/As to keep reaching the underlying syscall error.
func TestLockErrors_PreserveTheUnderlyingCause(t *testing.T) {
	_, err := filelock.Lock(blockedLockPath(t))
	require.Error(t, err)
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q was not wrapped with %%w; errors.Is/As cannot see the cause", err)
	}
}
