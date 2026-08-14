package agent

import (
	"fmt"
	"os"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// isOSBackedFs reports whether fs is the real operating-system filesystem, as
// opposed to a test double (afero.MemMapFs, a ReadOnlyFs wrapping one, ...).
// See WithFileLock for why this gates locking. Mirrors
// internal/shared/admission's identically-named, identically-reasoned helper
// (C5): both packages sit at the same leaf level and neither imports the
// other, so the four-line helper is duplicated rather than one leaf package
// reaching into its sibling for it.
func isOSBackedFs(fs afero.Fs) bool {
	_, ok := fs.(*afero.OsFs)
	return ok
}

// WithFileLock runs fn as ONE serialized read-modify-write transaction
// against target — an engine-owned settings file OUTSIDE any .ctxloom tree
// (a real home ~/.claude/settings.json, a project's .mcp.json,
// ~/.codex/config.toml, .kiro/settings/mcp.json, opencode.json, ...). It is
// the SettingsWriter family's counterpart to config.Manager.Update: hooks
// (SessionStart et al.), the MCP server, the CLI, the runner, and an
// in-container ctxloom (the same files bind-mounted) all read-modify-write
// these files, genuinely concurrently and today unlocked — two racing RMWs
// is a lost update on a file ctxloom does not own.
//
// fn is the WHOLE cycle — read, modify, write — never just the write: the
// lock has to be held before the read for "fresh" to mean anything. Every
// call site wraps its existing body (which already does its own read as the
// first real step) rather than splitting out a separate unlocked
// path-resolution phase the way config.Manager.Update does — these targets
// are deterministic paths derived from arguments already in hand, so there
// is nothing upstream of the read that needs to run before the lock exists.
//
// fs is the caller's OWN filesystem seam — the SAME value a caller's getFS()
// resolves from (nil meaning "real OS filesystem"; see GetFS) — and NOT
// necessarily nil by the time it reaches here, since some callers (e.g.
// agent.MCPFileConfig) already default-resolve before constructing.
// Locking is skipped entirely when fs is not OS-backed (isOSBackedFs):
// locking exists to exclude OTHER PROCESSES, which a test double
// (afero.MemMapFs and friends) has none of, and composing a lock path from
// one of its often-nonexistent, often-unwritable-by-this-user paths and
// asking the REAL OS to create and flock it would touch actual disk at an
// address the test never intended — exactly the crosstalk
// config.Manager.Update's injectedFS guard, and internal/shared/admission's
// identically-shaped useLock (C5), both exist to avoid.
//
// A lock ACQUISITION failure fails the whole call closed, matching
// config.Manager.Update's stance verbatim: filelock.Lock only errors on a
// persistent environmental failure (never ordinary contention, which it
// already waits out), so proceeding unlocked on that failure would silently
// discard the one guarantee this function exists to provide. The target file
// is left untouched on that path — fn never runs.
//
// The lock lives at filelock.HomePathFor(target), not filelock.PathFor(target)
// beside it — RULED 2026-08-13 (human), closing undated-bronco (fs-consolidation
// N1): a beside-the-file sidecar for a file this package does NOT own left
// untracked `.mcp.json.lock`/`.claude/settings.json.lock` litter in every
// project, and — worse — a ctxloom-owned file inside the user's REAL
// `~/.claude`/`~/.codex` home, a directory ctxloom otherwise never writes to
// at all. See filelock.HomePathFor's doc for the full reasoning.
func WithFileLock(fs afero.Fs, target string, fn func() error) error {
	if !isOSBackedFs(fs) {
		return fn()
	}
	lockPath, err := filelock.HomePathFor(target)
	if err != nil {
		return fmt.Errorf("agent: deriving home lock path for %s: %w", target, err)
	}
	unlock, err := filelock.Lock(lockPath)
	if err != nil {
		return fmt.Errorf("agent: acquiring settings lock for %s: %w", target, err)
	}
	defer unlock()
	cleanupLegacySidecar(target)
	return fn()
}

// cleanupLegacySidecar best-effort removes the beside-file sidecar
// (filelock.PathFor(target)) C6 left behind before this fix moved
// cross-binary locking to the home lock dir (undated-bronco, fs-consolidation
// N1). It runs AFTER the real lock is held, never before, so it cannot race
// this call's own critical section.
//
// Best-effort and silent on purpose: removing litter must never fail a write
// that would otherwise succeed, and a lock file carries no data — losing the
// race with another process's identical cleanup, or finding nothing there at
// all (the common case, once every process on the machine has run this once),
// are both unremarkable. os.Remove on a path another process still has open
// is safe on POSIX (unlink does not invalidate an open fd's flock); Windows'
// mandatory locking may leave the legacy sidecar behind if something else
// still holds it, which self-heals the next time nobody does.
func cleanupLegacySidecar(target string) {
	_ = os.Remove(filelock.PathFor(target))
}
