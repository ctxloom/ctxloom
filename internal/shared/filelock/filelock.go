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
	"fmt"
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

// Every error out of this package goes through one of the three wrappers
// below, which exist because the bare syscall errors do not say what was being
// attempted. "mkdir /home/u/.ctxloom: read-only file system" or "bad file
// descriptor" arriving at a caller that also reads, parses and writes the
// protected file is indistinguishable from a failure of that work — and the
// two want opposite responses: a lock failure means "someone else may be
// writing", a write failure means "your data did not land". Naming the lock
// path and the step is what lets the caller, and the operator reading the
// message, tell them apart. The wrappers live here, not in the two
// platform-specific files, so the wording cannot drift between them.

// errPrepare reports a failure to create the lock file's parent directory.
func errPrepare(path string, err error) error {
	return fmt.Errorf("filelock: prepare lock directory for %s: %w", path, err)
}

// errOpen reports a failure to open (or create) the lock file itself.
func errOpen(path string, err error) error {
	return fmt.Errorf("filelock: open lock file %s: %w", path, err)
}

// errAcquire reports a failure of the locking syscall — the file exists and
// was opened, but the lock could not be taken.
func errAcquire(path string, shared bool, err error) error {
	kind := "exclusive"
	if shared {
		kind = "shared"
	}
	return fmt.Errorf("filelock: acquire %s lock on %s: %w", kind, path, err)
}
