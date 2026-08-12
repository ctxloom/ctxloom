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
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
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

// lockSuffix is appended to a protected path to name its lock file. It is the
// package's real invariant and its most breakable one: two writers of the same
// resource that name the lock file differently do not exclude each other, and
// nothing reports it — no error, no warning, just two writers where there was
// meant to be one. Spelling the suffix at each call site is what allowed that
// to be a typo away, in four packages at once.
const lockSuffix = ".lock"

// PathFor returns the lock file that guards the given protected path, BESIDE
// that path. Callers pass the file they are protecting, not a lock name, so
// the convention lives here rather than being re-agreed at every acquisition.
//
// This is the shape for locks outside a project .ctxloom tree — the home-rooted
// stores under ~/.ctxloom (the session index, the task log, the project-id
// registry), whose sidecars nobody has to look at and which no `git status`
// ever reports. A file inside a PROJECT .ctxloom tree uses ProjectPathFor
// instead; see its doc for the boundary and why it exists.
func PathFor(protected string) string { return protected + lockSuffix }

// ProjectPathFor returns the lock file guarding protected, a file inside a
// PROJECT .ctxloom tree: <.ctxloom>/state/locks/<flattened relative path>.lock.
// It errors when protected is not inside a .ctxloom directory at all, because
// the alternative — inventing a location — is a lock that excludes nobody.
//
// WHY IT IS A FUNCTION AND NOT A JOIN AT THE CALL SITE. lockSuffix's comment
// states this package's real invariant and its most breakable one: two writers
// of the same resource that name the lock file differently do not exclude each
// other, and nothing reports it. A relocation is precisely where that breaks —
// every caller that composes its own <root>/state/locks/<name> is a chance to
// disagree about the root, the flattening, or the spelling of the path being
// flattened. So the whole derivation lives here, takes ONE argument (the file
// being protected, which the caller already holds), and derives everything else
// itself. There is nothing left for a call site to get wrong except calling the
// wrong function, which is one grep.
//
// THE BOUNDARY. Only project trees relocate. A home-rooted ~/.ctxloom store
// keeps the beside-the-file shape (PathFor) because the whole reason to move
// these was a `.ctxloom/config.yaml.lock` turning up untracked in a developer's
// freshly initialized project — a problem a lock under the user's own home
// directory does not have.
func ProjectPathFor(protected string) (string, error) {
	appDir, rel, err := splitAppDir(protected)
	if err != nil {
		return "", err
	}
	return filepath.Join(paths.LocksPath(appDir), flattenLockName(rel)+lockSuffix), nil
}

// splitAppDir resolves protected to an absolute, cleaned path and splits it at
// the DEEPEST enclosing .ctxloom directory, returning that directory and the
// path relative to it.
//
// Absolute-and-cleaned is what makes the mapping single-valued: a caller
// holding "./.ctxloom/config.yaml", ".ctxloom/content/../config.yaml" or the
// fully qualified path is holding the same file, and all three must reach the
// same lock. Deepest rather than outermost so a nested checkout (a worktree
// under a project, each with its own .ctxloom) locks in its own tree.
func splitAppDir(protected string) (appDir, rel string, err error) {
	abs, err := filepath.Abs(protected)
	if err != nil {
		return "", "", fmt.Errorf("filelock: resolve %s: %w", protected, err)
	}

	dir := filepath.Dir(abs)
	for {
		if filepath.Base(dir) == paths.AppDirName {
			relPath, relErr := filepath.Rel(dir, abs)
			if relErr != nil {
				return "", "", fmt.Errorf("filelock: relate %s to %s: %w", abs, dir, relErr)
			}
			return dir, relPath, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("filelock: %s is not inside a %s directory, so it has no project lock location", protected, paths.AppDirName)
		}
		dir = parent
	}
}

// flattenLockName turns a path relative to the .ctxloom root into a single
// filename component, so every lock in a project sits in ONE directory rather
// than in a shadow tree mirroring the real one.
//
// Collisions after flattening are SAFE and deliberately not defended against:
// two protected paths that flatten to one name share a lock and merely
// over-serialize, which costs a little concurrency. The failure this package
// cannot tolerate is the opposite one — one resource with two lock names — so
// the encoding is chosen to be total and deterministic rather than injective.
func flattenLockName(rel string) string {
	return strings.ReplaceAll(filepath.ToSlash(rel), "/", "__")
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
