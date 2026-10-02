package paths

import (
	"fmt"
	"path/filepath"
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
// engine's own settings.json/.mcp.json/config.toml, sitting either in a
// project directory (.mcp.json, .claude/settings.json) or in the user's REAL
// engine home (~/.claude/settings.json) — that more
// than one ctxloom-FAMILY BINARY (ctxloom, ltk, taskloom) may read-modify-write.
//
// It lives at ~/.ctxloom/locks/<flattened-absolute-protected-path>.lock:
// home-rooted so every binary resolves the identical lock location
// regardless of which one happens to run. Neither of this file's other two
// derivations fits: PathFor sits BESIDE the protected file, which for a file
// ctxloom does not own is exactly the litter this function exists to stop
// creating (RULED 2026-08-13, human; closes undated-bronco / the
// fs-consolidation plan's N1 finding — agent.WithFileLock's prior use of
// PathFor for these targets had been leaving `.mcp.json.lock`,
// `.claude/settings.json.lock`, and (worse) sidecars inside the user's REAL
// `~/.claude` home, untracked by any .gitignore pattern and, for the
// home-rooted one, a ctxloom-owned file inside a directory ctxloom
// otherwise never writes to at all); ProjectPathFor only resolves paths
// INSIDE a project .ctxloom tree, which a foreign file is by definition not.
//
// The protected path is flattened into a single bounded filename component
// by FlatName, the same way ProjectPathFor flattens a project-relative one.
// It is resolved to an absolute path FIRST so every spelling of one file
// reaches FlatName as the same string: the failure this package cannot
// tolerate is one resource with two lock names, excluding nobody.
//
// Unlike ProjectPathFor, HomePathFor never errors on WHERE protected is —
// there is no boundary to be outside of, since "foreign" means not required
// to sit inside any particular tree in the first place. The only failures
// are environmental: protected cannot be resolved to an absolute path, or
// the user's home directory cannot be resolved.
func HomePathFor(protected string) (string, error) {
	abs, err := filepath.Abs(protected)
	if err != nil {
		return "", fmt.Errorf("paths: resolve %s: %w", protected, err)
	}
	dir, err := HomeLocksDir()
	if err != nil {
		return "", fmt.Errorf("paths: resolve the home lock directory for %s: %w", protected, err)
	}
	return filepath.Join(dir, HomeLockName(abs)), nil
}

// HomeLockName is the leaf HomePathFor gives the lock guarding abs, an
// ALREADY-ABSOLUTE path in whichever filesystem names it. It is exported for
// the one caller that must name a lock in a filesystem this process is not
// running in — the container mount builder, which knows a file's
// in-container path and must land the host's lock file exactly where the
// container's own HomePathFor will look for it.
func HomeLockName(abs string) string { return FlatName(abs) + lockSuffix }
