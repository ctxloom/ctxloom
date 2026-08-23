//go:build !windows

package paths

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"
)

// The derivation is only worth anything if the file it names is the file that
// actually excludes. Held via PathFor(p), a lock must be unavailable to
// anything else asking for PathFor(p) — and must leave PathFor(q) alone.
//
// The probe is a non-blocking flock(2) rather than a second blocking Lock:
// a real acquisition on PathFor(protected) is deliberately blocking, so
// probing with it would park instead of answering.
func TestPathFor_LockOnAResourceExcludesThatResourceAndNoOther(t *testing.T) {
	dir := t.TempDir()
	protected := filepath.Join(dir, "index.json")
	other := filepath.Join(dir, "other.json")

	held := flock.New(PathFor(protected))
	require.NoError(t, held.Lock())

	require.False(t, lockFree(t, PathFor(protected)),
		"two writers of the same resource did not exclude each other")
	require.True(t, lockFree(t, PathFor(other)),
		"locking one resource blocked an unrelated one")

	require.NoError(t, held.Unlock())
	require.True(t, lockFree(t, PathFor(protected)), "the lock was not released")
}

// lockFree reports whether an exclusive lock on path can be taken right now,
// without waiting for one that is held.
func lockFree(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}
