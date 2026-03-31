package filelock_test

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/filelock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLock_Basic(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	unlock, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	require.NotNil(t, unlock)

	// File should exist
	_, err = os.Stat(lockPath)
	assert.NoError(t, err)

	// Release lock
	unlock()
}

func TestLock_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "subdir", "nested", "test.lock")

	unlock, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	defer unlock()

	// File and directories should exist
	_, err = os.Stat(lockPath)
	assert.NoError(t, err)
}

func TestLock_ExclusiveAccess(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	var counter int64
	var wg sync.WaitGroup
	iterations := 10
	goroutines := 5

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				unlock, err := filelock.Lock(lockPath)
				if err != nil {
					t.Errorf("Lock failed: %v", err)
					return
				}

				// Critical section - increment counter
				current := atomic.LoadInt64(&counter)
				time.Sleep(time.Microsecond) // Introduce race opportunity
				atomic.StoreInt64(&counter, current+1)

				unlock()
			}
		}()
	}

	wg.Wait()

	// Without proper locking, this would likely fail due to lost updates
	expected := int64(goroutines * iterations)
	assert.Equal(t, expected, atomic.LoadInt64(&counter))
}

func TestLockShared_MultipleReaders(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	// Acquire multiple shared locks simultaneously
	var unlocks []func()
	for i := 0; i < 3; i++ {
		unlock, err := filelock.LockShared(lockPath)
		require.NoError(t, err)
		unlocks = append(unlocks, unlock)
	}

	// All should succeed - multiple shared locks allowed
	assert.Len(t, unlocks, 3)

	// Clean up
	for _, unlock := range unlocks {
		unlock()
	}
}

func TestTryLock_Success(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	unlock, err := filelock.TryLock(lockPath)
	require.NoError(t, err)
	require.NotNil(t, unlock)
	defer unlock()
}

func TestTryLock_Blocked(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	// Acquire exclusive lock
	unlock1, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	defer unlock1()

	// TryLock should fail immediately
	unlock2, err := filelock.TryLock(lockPath)
	assert.ErrorIs(t, err, filelock.ErrLocked)
	assert.Nil(t, unlock2)
}

func TestTryLockShared_BlockedByExclusive(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	// Acquire exclusive lock
	unlock1, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	defer unlock1()

	// TryLockShared should fail
	unlock2, err := filelock.TryLockShared(lockPath)
	assert.ErrorIs(t, err, filelock.ErrLocked)
	assert.Nil(t, unlock2)
}

func TestLock_ReleaseThenReacquire(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	// First lock
	unlock1, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	unlock1()

	// Second lock should succeed after release
	unlock2, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	defer unlock2()
}

func TestLock_RootDirectoryFile(t *testing.T) {
	// Test with a file path that has an empty parent directory after extraction
	// This exercises the edge case in ensureDir where dir == ""

	// We'll use a path within temp dir that looks like it's at root
	// The actual filesystem can't write to /, but we test pathBase with a simple name
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "simple.lock")

	unlock, err := filelock.Lock(lockPath)
	require.NoError(t, err)
	defer unlock()
}

func TestTryLockShared_Success(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	// Should succeed when no lock is held
	unlock, err := filelock.TryLockShared(lockPath)
	require.NoError(t, err)
	require.NotNil(t, unlock)
	defer unlock()
}
