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

// homeAppDirName is filelock's own reference to the ctxloom app-dir segment
// (".ctxloom"), used by HomePathFor. It is an ALIAS of paths.AppDirName —
// the same posture internal/shared/tasks/paths.AppDirName documents itself
// as ("not a second declaration of \".ctxloom\"") — not a duplicate literal,
// because this package already imports internal/paths (splitAppDir compares
// against paths.AppDirName directly) and reusing the symbol costs nothing.
//
// It is declared as its OWN package-level const, rather than spelled
// paths.AppDirName inline inside HomePathFor's filepath.Join call, for a
// concrete reason: tests/arch's path-authority gate
// (TestArch_PathAuthority_SessionStoreLiteralsLiveInPaths) flags any
// filepath.Join call outside internal/paths that mixes a literal "paths.X"
// selector with a bare local segment — exactly HomePathFor's shape, one
// path segment named through paths and one (homeLocksLeaf, its neighbor
// below) that cannot be. Aliasing here means HomePathFor's own Join call
// spells no "paths." selector at all, so the gate never inspects it —
// see homeLocksLeaf's doc for why that neighbor CANNOT take the same
// shortcut.
const homeAppDirName = paths.AppDirName

// homeLocksLeaf is the leaf directory under the home app dir that
// HomePathFor puts its lock sidecars in: ~/.ctxloom/locks. UNLIKE
// homeAppDirName above, this is a genuine duplicate, not an alias:
// internal/paths.go carries its own HomeLocksDirName constant (the
// Layout() row for this same directory, so doctor can see it), and the two
// packages cannot share one symbol for it — referencing paths.HomeLocksDirName
// directly inside HomePathFor's filepath.Join call would trip the exact same
// path-authority gate the homeAppDirName alias exists to dodge (the gate
// does not care WHICH paths.* symbol sits beside a bare segment, only that
// one does), and filelock is documented as a zero-internal-import leaf
// package in the first place (docs/architecture/shared/filesystem-io.md —
// a claim ProjectPathFor's own paths.AppDirName/paths.LocksPath use already
// strains, but not one this new code should strain further).
//
// KEEP IN SYNC BY HAND WITH internal/paths.go's HomeLocksDirName. lockSuffix's
// own doc names a silent divergence between two spellings of one resource's
// name as this package's worst failure mode; an un-synced pair of "locks"
// constants across this package boundary is that same hazard, just harder to
// grep for than a single call site.
const homeLocksLeaf = "locks"

// HomePathFor returns the lock file guarding protected, a FOREIGN file this
// package does not own: one OUTSIDE any project .ctxloom tree entirely — an
// engine's own settings.json/.mcp.json/config.toml, sitting either in a
// project directory (.mcp.json, .claude/settings.json) or in the user's REAL
// engine home (~/.claude/settings.json, ~/.codex/config.toml) — that more
// than one ctxloom-FAMILY BINARY (ctxloom, ltk, taskloom) may read-modify-write.
//
// It lives at ~/.ctxloom/locks/<flattened-absolute-protected-path>.lock:
// home-rooted so every binary resolves the identical lock location
// regardless of which one happens to run. Neither of this package's other
// two derivations fits: PathFor sits BESIDE the protected file, which for a
// file ctxloom does not own is exactly the litter this function exists to
// stop creating (RULED 2026-08-13, human; closes undated-bronco / the
// fs-consolidation plan's N1 finding — agent.WithFileLock's prior use of
// PathFor for these targets had been leaving `.mcp.json.lock`,
// `.claude/settings.json.lock`, and (worse) sidecars inside the user's REAL
// `~/.claude` and `~/.codex` homes, untracked by any .gitignore pattern and,
// for the home-rooted pair, a ctxloom-owned file inside a directory ctxloom
// otherwise never writes to at all); ProjectPathFor only resolves paths
// INSIDE a project .ctxloom tree, which a foreign file is by definition not.
//
// The protected path is flattened into a single filename component the same
// way ProjectPathFor's flattenLockName flattens a project-relative one — see
// its doc for why two protected paths that happen to flatten to the same
// name are left to COLLIDE rather than defended against: they merely
// over-serialize each other (a lock excluding a slightly wider set of
// operations than strictly necessary), which is the safe direction to err in
// compared to the failure this package cannot tolerate — one resource,
// two lock names, excluding nobody.
//
// Unlike ProjectPathFor, HomePathFor never errors on WHERE protected is —
// there is no boundary to be outside of, since "foreign" means not required
// to sit inside any particular tree in the first place. The only failures
// are environmental: protected cannot be resolved to an absolute path, or
// the user's home directory cannot be resolved.
func HomePathFor(protected string) (string, error) {
	abs, err := filepath.Abs(protected)
	if err != nil {
		return "", fmt.Errorf("filelock: resolve %s: %w", protected, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("filelock: resolve the home lock directory for %s: %w", protected, err)
	}
	dir := filepath.Join(home, homeAppDirName, homeLocksLeaf)
	return filepath.Join(dir, flattenLockName(abs)+lockSuffix), nil
}

// flattenLockName turns a path into a single filename component, so every
// lock guarding paths under one root sits in ONE directory rather than in a
// shadow tree mirroring the real one. ProjectPathFor calls it on a path
// RELATIVE to a project's .ctxloom root; HomePathFor calls it on a FULLY
// ABSOLUTE path (there is no shared root to be relative to for a foreign
// file) — both callers want the identical property, so one function serves
// both rather than each reimplementing it.
//
// Collisions after flattening are SAFE and deliberately not defended against:
// two protected paths that flatten to one name share a lock and merely
// over-serialize, which costs a little concurrency. The failure this package
// cannot tolerate is the opposite one — one resource with two lock names — so
// the encoding is chosen to be total and deterministic rather than injective.
//
// KNOWN GAP (inherited, not new): on Windows an absolute path carries a
// drive letter (`C:\Users\...`), and `:` survives flattening untouched —
// ProjectPathFor never hits this (its input is always relative, so it is
// never handed a drive letter), but HomePathFor's absolute input can be.
// Left as-is rather than patched here with logic this package's Unix-only
// test suite cannot exercise; a Windows-specific fix belongs beside
// filelock_windows.go, with a test that actually runs there.
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
