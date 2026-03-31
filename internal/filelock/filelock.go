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

import "os"

// Lock acquires an exclusive (write) lock on the specified file.
// The file is created if it doesn't exist.
// Returns an unlock function that must be called to release the lock.
// The lock is blocking - it will wait until the lock can be acquired.
func Lock(path string) (unlock func(), err error) {
	return lockFile(path, false)
}

// LockShared acquires a shared (read) lock on the specified file.
// Multiple readers can hold shared locks simultaneously.
// The file is created if it doesn't exist.
// Returns an unlock function that must be called to release the lock.
func LockShared(path string) (unlock func(), err error) {
	return lockFile(path, true)
}

// ensureDir creates the parent directory of path if it doesn't exist.
func ensureDir(path string) error {
	dir := path[:len(path)-len(pathBase(path))-1]
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0755)
}

// pathBase returns the last element of path (simple implementation to avoid filepath import).
func pathBase(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
