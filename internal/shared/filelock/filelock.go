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

// lockSuffix is appended to a protected path to name its lock file. It is the
// package's real invariant and its most breakable one: two writers of the same
// resource that name the lock file differently do not exclude each other, and
// nothing reports it — no error, no warning, just two writers where there was
// meant to be one. Spelling the suffix at each call site is what allowed that
// to be a typo away, in four packages at once.
const lockSuffix = ".lock"

// PathFor returns the lock file that guards the given protected path. Callers
// pass the file they are protecting, not a lock name, so the convention lives
// here rather than being re-agreed at every acquisition.
func PathFor(protected string) string { return protected + lockSuffix }

// lockFileMode and lockDirMode are the modes a lock file and its parent
// directory are CREATED with, before umask. They are the conventional file /
// directory pair — a directory needs the execute bit to be traversable at all,
// which is the whole of the difference between them — and both are deliberately
// not group- or world-WRITABLE.
//
// That last part bounds who can take these locks: acquiring one opens the file
// O_RDWR, so only the owner can. On a project .ctxloom shared between UNIX
// accounts a second user's acquisition fails outright rather than silently
// sharing. Widening these is a security decision about who may block whom, not
// a formatting one, and is not made here.
const (
	lockFileMode = 0o644
	lockDirMode  = 0o755
)

// ensureDir creates the parent directory of path if it doesn't exist.
func ensureDir(path string) error {
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	return os.MkdirAll(dir, lockDirMode)
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
