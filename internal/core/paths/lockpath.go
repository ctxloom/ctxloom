package paths

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

// lockSuffix is appended to a protected path to name its lock file. It is
// this file's real invariant and its most breakable one: two writers of the
// same resource that name the lock file differently do not exclude each
// other, and nothing reports it — no error, no warning, just two writers
// where there was meant to be one. Spelling the suffix at each call site is
// what allowed that to be a typo away, in four packages at once.
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
// states this file's real invariant and its most breakable one: two writers
// of the same resource that name the lock file differently do not exclude
// each other, and nothing reports it. A relocation is precisely where that
// breaks — every caller that composes its own <root>/state/locks/<name> is a
// chance to disagree about the root, the flattening, or the spelling of the
// path being flattened. So the whole derivation lives here, takes ONE
// argument (the file being protected, which the caller already holds), and
// derives everything else itself. There is nothing left for a call site to
// get wrong except calling the wrong function, which is one grep.
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
	return filepath.Join(LocksPath(appDir), FlatName(rel)+lockSuffix), nil
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
		return "", "", fmt.Errorf("paths: resolve %s: %w", protected, err)
	}

	dir := filepath.Dir(abs)
	for {
		if filepath.Base(dir) == AppDirName {
			relPath, relErr := filepath.Rel(dir, abs)
			if relErr != nil {
				return "", "", fmt.Errorf("paths: relate %s to %s: %w", abs, dir, relErr)
			}
			return dir, relPath, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("paths: %s is not inside a %s directory, so it has no project lock location", protected, AppDirName)
		}
		dir = parent
	}
}

// HomePathFor returns the lock file guarding protected, a FOREIGN file this
// package does not own: one OUTSIDE any project .ctxloom tree entirely — an
// engine's own settings.json/.mcp.json/config.toml, sitting in a project
// directory (.mcp.json, .claude/settings.json), in the user's REAL engine
// home (~/.claude/settings.json) or in a session's engine home — that more
// than one ctxloom-FAMILY BINARY (ctxloom, ltk, taskloom) may
// read-modify-write.
//
// Where the lock lives depends on where protected is:
//
//   - inside a session dir, ~/.ctxloom/sessions/<harp>/<rel>: at
//     <harp>/locks/<flattened rel>.lock (HarpLocksDir). A session mints its
//     own set of these and nothing outside it ever reuses one, so they are a
//     member of the session (the HarpLocksDirName row of HarpMembers) and the
//     reaper takes them with it. In the shared home locks dir a flattened
//     name cannot say which session it served, so nothing could remove them.
//   - anywhere else: at ~/.ctxloom/locks/<flattened absolute path>.lock
//     (HomeLocksDir).
//
// Both are home-rooted, so every binary resolves the identical lock location
// whichever one happens to run, and both are derived from protected alone,
// so every caller that locks one file through this function reaches one
// lock. Neither of this file's other two derivations fits: PathFor sits
// BESIDE the protected file, which for a file ctxloom does not own is litter
// — `.mcp.json.lock` and `.claude/settings.json.lock` untracked in every
// project, and a ctxloom-owned file inside the user's real `~/.claude`, a
// directory ctxloom otherwise never writes to; ProjectPathFor only resolves
// paths INSIDE a project .ctxloom tree, which a foreign file is by
// definition not.
//
// "Inside a session dir" means strictly below a harp dir — a name
// harp.Validate accepts, directly under the sessions root. The sessions root
// itself, a harp dir itself, and the files beside the harp dirs (<harp>.lock
// and its siblings) are not: a lock guarding a whole session dir must not
// live inside the dir it guards (the reason HarpLockPath sits beside it),
// and the root and its loose files belong to no session.
//
// The protected path is flattened into a single bounded filename component
// by FlatName — relative to its harp dir for a session file, absolute
// otherwise — the same way ProjectPathFor flattens a project-relative one.
// It is resolved to an absolute path FIRST so every spelling of one file
// reaches FlatName as the same string: the failure this package cannot
// tolerate is one resource with two lock names, excluding nobody. A session
// file is recognized under the sessions root both as $HOME spells it and as
// its symlinks resolve, so a caller holding the resolved spelling of a file
// under a symlinked HOME (git reports worktree paths that way) still reaches
// the session's one lock. protected itself is never resolved: whether a link
// along its path exists yet would then decide which lock it names.
//
// Unlike ProjectPathFor, HomePathFor never errors on WHERE protected is —
// there is no boundary to be outside of, since "foreign" means not required
// to sit inside any particular tree in the first place. The only failures
// are environmental: protected cannot be resolved to an absolute path, the
// user's home directory cannot be resolved, or — under a test binary — the
// lock would land in the account's real home (see HomeLocksDir).
//
// HomeLockPathIn is the same derivation for a filesystem this process is not
// running in.
func HomePathFor(protected string) (string, error) {
	abs, err := filepath.Abs(protected)
	if err != nil {
		return "", fmt.Errorf("paths: resolve %s: %w", protected, err)
	}
	lock, inSession, err := harpLockFor(abs)
	if err != nil {
		return "", fmt.Errorf("paths: resolve the session lock directory for %s: %w", protected, err)
	}
	if inSession {
		return lock, nil
	}
	dir, err := HomeLocksDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve the home lock directory for %s: %w", protected, err)
	}
	return filepath.Join(dir, homeLockName(abs)), nil
}

// harpLockFor is HomePathFor's session case on this process's own
// filesystem: the lock guarding abs (absolute and clean) when it lies inside
// a session dir, matched under the sessions root as $HOME spells it and,
// failing that, as the root resolves. The lock is always built under the
// root as $HOME spells it, so both spellings of one file name one lock: the
// one HarpLocksDir and the reaper's layout name. A home that cannot be
// resolved matches nothing; HomeLocksDir then fails on the same cause.
func harpLockFor(abs string) (lock string, inSession bool, err error) {
	root, err := HomeSessionsDir()
	if err != nil {
		return "", false, nil
	}
	if root, err = filepath.Abs(root); err != nil {
		return "", false, nil
	}
	harp, rel, ok := splitHarpPath(filepath.ToSlash(root), filepath.ToSlash(abs))
	if !ok {
		resolved, rerr := filepath.EvalSymlinks(root)
		if rerr != nil || resolved == root {
			return "", false, nil
		}
		if harp, rel, ok = splitHarpPath(filepath.ToSlash(resolved), filepath.ToSlash(abs)); !ok {
			return "", false, nil
		}
	}
	lock = filepath.Join(root, filepath.FromSlash(harpLockRel(harp, rel)))
	if err := accountHomeError("session lock directory", filepath.Dir(lock), sandboxedHomeRemedy); err != nil {
		return "", false, err
	}
	return lock, true, nil
}

// splitHarpPath splits abs at the harp dir it lies strictly inside, directly
// under sessionsDir, returning the harp and abs relative to that dir. Both
// paths are absolute, clean and slash-separated. ok is false for sessionsDir
// itself, a harp dir itself, a name beside the harp dirs, a first component
// harp.Validate refuses, and every path outside sessionsDir.
func splitHarpPath(sessionsDir, abs string) (harp, rel string, ok bool) {
	under, found := strings.CutPrefix(abs, strings.TrimSuffix(sessionsDir, "/")+"/")
	if !found {
		return "", "", false
	}
	harp, rel, found = strings.Cut(under, "/")
	if !found || rel == "" || harpid.Validate(harp) != nil {
		return "", "", false
	}
	return harp, rel, true
}

// harpLockRel is the lock guarding rel, a path inside harp's dir, relative to
// the sessions root and slash-separated: <harp>/locks/<FlatName(rel)>.lock.
// Flattening the path relative to the harp dir, not the absolute one, keeps
// the name independent of where the sessions root is and how it is spelled.
func harpLockRel(harp, rel string) string {
	return path.Join(harp, HarpLocksDirName, FlatName(rel)+lockSuffix)
}

// homeLockName is the leaf, in the home locks dir, of the lock guarding abs:
// an ALREADY-ABSOLUTE path outside every session dir.
func homeLockName(abs string) string { return FlatName(abs) + lockSuffix }

// LockReapInterlockPath returns the reap interlock: the one file every
// activity that takes or binds a sidecar of the home lock directory holds
// SHARED, and the reap of that directory (`ctxloom clean`) holds EXCLUSIVE,
// so a reap never runs beside a container launch binding host sidecars or a
// write taking one. It sits in the directory it guards, so it follows that
// directory wherever it resolves (HomeLocksDir's override and guard
// included), and a reap of that directory never removes it.
func LockReapInterlockPath() (string, error) {
	dir, err := HomeLocksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, LockReapInterlockFileName), nil
}

// HomeLocksDirIn is HomeLocksDir in a filesystem this process is not running
// in, whose home directory is home (slash-separated, as a container names
// it): <home>/.ctxloom/locks.
func HomeLocksDirIn(home string) string { return path.Join(home, AppDirName, HomeLocksDirName) }

// HomeLockPathIn is HomePathFor in a filesystem this process is not running
// in: the lock a process whose home directory is home gives abs. Both are
// absolute and slash-separated, as a container names them, and abs is
// cleaned as HomePathFor cleans it.
//
// It exists for the one caller that must place a lock where ANOTHER
// filesystem's own HomePathFor will look — the container mount builder,
// which knows a file's in-container path and binds the host's lock file
// there. Composing that path at the call site would be a second copy of this
// file's derivation, free to disagree with the first about the session split
// or the flattening; a disagreement there is two lock files for one resource,
// excluding nobody, and nothing reports it.
//
// It resolves no symlink, since it cannot see the filesystem it names, so it
// agrees with that filesystem's HomePathFor wherever that filesystem's
// sessions root is not reached through a link — true of a container home,
// which the image bakes as a real directory.
func HomeLockPathIn(home, abs string) string {
	abs = path.Clean(abs)
	sessions := path.Join(home, AppDirName, SessionsDir)
	if harp, rel, ok := splitHarpPath(sessions, abs); ok {
		return path.Join(sessions, harpLockRel(harp, rel))
	}
	return path.Join(HomeLocksDirIn(home), homeLockName(abs))
}
