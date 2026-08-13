package filelock_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// TestTryLock_AcquiresWhenFree pins the ordinary case: nobody else holds the
// lock, so TryLock behaves like Lock — it succeeds immediately and returns a
// callable release.
func TestTryLock_AcquiresWhenFree(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	unlock, acquired, err := filelock.TryLock(lockPath)
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, unlock)
	unlock()
}

// TestTryLock_DoesNotBlockOnContention is the whole reason TryLock exists
// over Lock: a second acquisition attempt while the first is held must
// return immediately with acquired=false and NO error — contention is a
// normal outcome a caller acts on, not a failure.
func TestTryLock_DoesNotBlockOnContention(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	holder, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	defer holder()

	unlock, acquired, err := filelock.TryLock(lockPath)
	require.NoError(t, err, "contention is not an error")
	assert.False(t, acquired)
	require.NotNil(t, unlock, "unlock must stay callable even when nothing was acquired")
	unlock() // must be a safe no-op
}

// TestTryLock_ContendsAgainstASharedLockToo asserts TryLock's exclusivity:
// it must fail to acquire even against a SHARED lock held by someone else —
// this is the property Recorder's ownership probe (a shared lock) and
// RefreshVendorTranscript's rebuild probe (TryLock) depend on to exclude
// each other.
func TestTryLock_ContendsAgainstASharedLockToo(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	holder, err := filelock.LockShared(lockPath)
	require.NoError(t, err)
	defer holder()

	unlock, acquired, err := filelock.TryLock(lockPath)
	require.NoError(t, err)
	assert.False(t, acquired, "an exclusive TryLock must not be granted while a shared lock is held elsewhere")
	unlock()
}

// TestTryLock_SucceedsAfterHolderReleases confirms the probe is a genuine
// point-in-time check, not a permanent refusal: once the contending holder
// releases, a later TryLock on the same path succeeds.
func TestTryLock_SucceedsAfterHolderReleases(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	holder, err := filelock.Lock(lockPath)
	require.NoError(t, err)

	_, acquired, err := filelock.TryLock(lockPath)
	require.NoError(t, err)
	require.False(t, acquired)

	holder()

	unlock, acquired, err := filelock.TryLock(lockPath)
	require.NoError(t, err)
	require.True(t, acquired)
	unlock()
}

// TestTryLock_FailureStillReturnsACallableRelease mirrors
// TestLock_FailureStillReturnsACallableRelease (errors_test.go /
// unlock_contract_test.go): every acquisition function in this package,
// including TryLock, must hand back a safe-to-call-unconditionally release
// even on an environmental failure, so `unlock, _, err := TryLock(p); defer
// unlock()` never panics.
func TestTryLock_FailureStillReturnsACallableRelease(t *testing.T) {
	unlock, acquired, err := filelock.TryLock(blockedLockPath(t))
	require.Error(t, err, "the fixture must make acquisition fail")
	require.False(t, acquired)
	require.NotNil(t, unlock, "a failed acquisition must still return a callable release")
	unlock()
	unlock() // and it must stay callable: releasing nothing, twice, is not an error
}
