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
	"time"
)

// Lock acquires an exclusive (write) lock on the specified file.
// The file is created if it doesn't exist.
// Returns an unlock function that must be called to release the lock.
// The lock is blocking - it will wait until the lock can be acquired. A wait
// that runs long reports itself to stderr and keeps waiting: see noteWaiting.
//
// unlock is NEVER nil, including on error: see releaseOrNoop.
func Lock(path string) (unlock func(), err error) {
	stop := noteWaiting(path)
	defer stop()
	return releaseOrNoop(lockFile(path, false))
}

// LockShared acquires a shared (read) lock on the specified file.
// Multiple readers can hold shared locks simultaneously.
// The file is created if it doesn't exist.
// Returns an unlock function that must be called to release the lock.
//
// unlock is NEVER nil, including on error: see releaseOrNoop.
func LockShared(path string) (unlock func(), err error) {
	stop := noteWaiting(path)
	defer stop()
	return releaseOrNoop(lockFile(path, true))
}

// TryLock attempts to acquire an exclusive (write) lock on path WITHOUT
// blocking. The file is created if it doesn't exist.
//
// acquired=false is a NORMAL outcome — someone else holds the lock right
// now — not an error: the whole reason to reach for TryLock instead of Lock
// is a caller with useful work to do under contention (skip a rebuild the
// current owner's writes make redundant, let a concurrent attempt of the
// same operation stand down) rather than one that must wait its turn. err is
// reserved for environmental failure — can't create the lock directory,
// can't open the lock file, or the locking syscall itself failed for a
// reason OTHER than contention — and means the same fail-closed thing Lock's
// error does.
//
// unlock is NEVER nil, including when acquired is false or err is non-nil:
// see releaseOrNoop. A caller may defer unlock() immediately after the call
// — the same shape Lock/LockShared document — without first checking
// acquired.
func TryLock(path string) (unlock func(), acquired bool, err error) {
	u, acquired, err := tryLockFile(path)
	unlock, err = releaseOrNoop(u, err)
	return unlock, acquired, err
}

// waitNoticeAfter is how long an acquisition may block before it says so. Long
// enough that ordinary contention — one writer appending a line — never
// prints, short enough that a human who has just run a command and is watching
// it sit there gets the notice while they are still watching.
const waitNoticeAfter = 3 * time.Second

// noteWaiting starts the watchdog that makes a blocked acquisition VISIBLE,
// and returns the function that stands it down.
//
// Acquisition here is unconditionally blocking (flock(2) with no LOCK_NB;
// LockFileEx with no fail-immediately flag), so a holder that never releases
// parks the caller forever — `taskloom status` reaches this on its exclusive
// write path and simply never returns. Whether that wait should instead FAIL
// is a per-call-site policy question, and this package cannot answer it alone:
// its callers disagree today about whether a failure to acquire means "stop"
// or "proceed unlocked". So the wait stays unbounded. What it no longer is is
// silent — the difference between an unexplained hang and one an operator can
// diagnose and end, which is the half of the harm that needs no decision.
//
// Only the watchdog goroutine reports; the caller stays parked in the syscall,
// so nothing about the acquisition itself changes. The returned stop waits for
// the watchdog to finish, so no notice can surface after the acquisition has
// already returned.
func noteWaiting(path string) (stop func()) {
	settled := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		timer := time.NewTimer(waitNoticeAfter)
		defer timer.Stop()
		select {
		case <-settled:
		case <-timer.C:
			fmt.Fprintf(os.Stderr, "filelock: still waiting for lock on %s\n", path)
		}
	}()
	return func() {
		close(settled)
		<-finished
	}
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
