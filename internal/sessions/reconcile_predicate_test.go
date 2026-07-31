//go:build unix

package sessions

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestReconcile_PredicateRunsUnderBothLocks pins the constraint Reconcile's
// doc now states: isDead is invoked while the Manager holds BOTH its mutex and
// the index file lock, so a predicate that reaches back into the session index
// hangs the process instead of failing. A caller cannot discover that from the
// signature -- it takes a plain func(Entry) bool -- and the failure has no
// error and no output.
//
// The pin establishes the hazard without ever hanging the suite: from inside
// the predicate it takes the two observations that prove it, a failed
// non-blocking TryLock on the Manager mutex and a failed non-blocking flock on
// the index lock file. Neither call waits, so a red version of this test fails
// rather than parking (a red test that parks burns a full tool timeout).
func TestReconcile_PredicateRunsUnderBothLocks(t *testing.T) {
	testsupport.Isolate(t)

	path := filepath.Join(t.TempDir(), "index.yaml")
	m, err := Open(path)
	require.NoError(t, err)
	_, err = m.AssignHarp("/proj", "claude")
	require.NoError(t, err)

	var (
		calls        int
		mutexHeld    bool
		fileLockHeld bool
		reconcileErr error
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, reconcileErr = m.Reconcile(func(Entry) bool {
			calls++
			// If TryLock SUCCEEDED the predicate would not be running under
			// the Manager mutex at all, and the contract would be wrong.
			if m.mu.TryLock() {
				m.mu.Unlock()
			} else {
				mutexHeld = true
			}
			fileLockHeld = !flockAvailable(path + ".lock")
			return false
		})
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Reconcile did not return within 30s; the predicate must not block")
	}

	require.NoError(t, reconcileErr)
	require.Equal(t, 1, calls, "the predicate must have actually run, or nothing below was measured")
	require.True(t, mutexHeld,
		"isDead runs with the Manager mutex held: a predicate calling back into this Manager self-deadlocks")
	require.True(t, fileLockHeld,
		"isDead runs with the index file lock held: a predicate opening a second Manager over the same file blocks forever")
}

// flockAvailable reports whether an exclusive flock on path could be taken
// right now WITHOUT waiting. LOCK_NB is what makes it safe to call from inside
// a predicate that already runs under that very lock.
func flockAvailable(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

// TestFlockAvailable_DetectsAnUnheldLock is the fixture's own sanity check: the
// probe above must report TRUE on a lock nobody holds. Without this, a probe
// that always returned false would make the assertion above pass for the wrong
// reason.
func TestFlockAvailable_DetectsAnUnheldLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "free.lock")
	require.True(t, flockAvailable(p), "an unheld lock must probe as available")
}
