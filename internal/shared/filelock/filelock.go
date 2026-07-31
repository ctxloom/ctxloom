// Package filelock provides cross-platform file locking for concurrent access control.
//
// On Unix systems, this uses flock(2) for advisory locking.
// On Windows, this uses LockFileEx for mandatory locking.
//
// Usage:
//
//	unlock, err := filelock.Lock("/path/to/file.lock")
//	if err != nil {
//	    return err
//	}
//	defer unlock()
//	// ... exclusive access to protected resource
package filelock

import (
	"os"
	"path/filepath"
)

// Lock acquires an exclusive (write) lock on the specified file.
// The file is created if it doesn't exist.
// Returns an unlock function that must be called to release the lock.
// The lock is blocking - it will wait until the lock can be acquired.
//
// unlock is NEVER nil, including on error: see releaseOrNoop.
func Lock(path string) (unlock func(), err error) {
	return releaseOrNoop(lockFile(path, false))
}

// LockShared acquires a shared (read) lock on the specified file.
// Multiple readers can hold shared locks simultaneously.
// The file is created if it doesn't exist.
// Returns an unlock function that must be called to release the lock.
//
// unlock is NEVER nil, including on error: see releaseOrNoop.
func LockShared(path string) (unlock func(), err error) {
	return releaseOrNoop(lockFile(path, true))
}

// releaseOrNoop guarantees the returned release function is callable whatever
// happened. The documented caller shape defers the release immediately —
//
//	unlock, err := filelock.Lock(p)
//	defer unlock()
//	if err != nil { ... }
//
// so the release runs on the error path too, and a nil there turns "the lock
// could not be taken" into a nil dereference in the caller. Failing to
// acquire is the caller's business; crashing is not. Releasing a lock that
// was never held is a no-op, so the substitute has nothing to do.
func releaseOrNoop(unlock func(), err error) (func(), error) {
	if unlock == nil {
		return func() {}, err
	}
	return unlock, err
}

// ensureDir creates the parent directory of path if it doesn't exist.
func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
