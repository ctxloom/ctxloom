package operations

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

// SessionHomeReapResult tallies one ReapOrphanedSessionHomes sweep, for a
// one-line boot-transcript summary — the same shape, and the same purpose, as
// isolation.WorktreeReapResult.
//
// Reaped and Skipped count CANDIDATES only: directories under state/ that this
// sweep recognised as per-session engine-home instances. Everything else under
// state/ (root files, the fixed locks/ and trust/ residents, symlinks, any
// directory that is not shaped like an instance) is not a candidate at all and
// is counted nowhere — "skipped" would imply the sweep considered removing it.
type SessionHomeReapResult struct {
	// Reaped is the number of instances actually removed.
	Reaped int
	// Skipped is the number of instances left in place: their session is
	// LIVE, or removal was attempted and failed (warned, never fatal).
	Skipped int
}

// ReapOrphanedSessionHomes removes <appPath>/state/<harp> directories whose
// harp is not a live session, per the session index.
//
// THIS IS A SECURITY SWEEP, NOT HYGIENE. Every instance holds a CREDENTIAL
// copied one-way out of the user's real host home (isolation.PrepareClaudeHome
// / PrepareCodexHome), so an un-reaped instance leaves credential bytes sitting
// inside the project tree — one per session, forever. EndSession removes an
// instance at graceful session end; this is the backstop for the ungraceful
// ones, mirroring internal/cli's worktree sweep, whose own doc says it exists
// because "teardown() only ever runs on a graceful Cleanup()". The same is true
// here, so the same backstop is required.
//
// LIVENESS, and why it is the session index. An instance is the config home an
// engine is RUNNING AGAINST, so reaping a live session's instance yanks its
// engine's config (and credential) out from under it mid-run — a concurrent
// session's sweep must never do that. The index is the only cross-process
// record of which harps exist, so:
//
//	a harp is LIVE ⇔ the index carries an entry for it whose EndedAt is nil.
//
// That predicate errs in the safe direction and only in the safe direction: a
// running session is stamped ended only by EndSession, at its own end, so a
// live session's entry ALWAYS has a nil EndedAt and is ALWAYS skipped. The
// cost of that conservatism is stated plainly: a session whose process died
// without reaching EndSession keeps a nil EndedAt too, so its instance reads as
// live and this sweep never reaps it. Distinguishing those two would need a
// liveness signal the index does not carry (the worktree reaper has one — a
// sibling owner-pid marker — because it was built with one); the sessions that
// DO get reaped here are the ones whose end-mark landed but whose removal did
// not, and the ones whose harp the index no longer carries at all.
//
// NO INDEX, NO SWEEP. An unreadable index is an error and removes nothing: a
// sweep with no liveness signal would classify every live session as an orphan
// and reap the whole project's engine state.
//
// THE CANDIDATE SET IS AN ALLOW-SHAPE, and every exclusion is pinned by a test
// in session_home_reap_test.go. A candidate is a directory DIRECTLY under
// state/ that is
//
//   - not a symlink — following one would turn a sweep of a disposable project
//     directory into a delete of whatever it points at;
//   - not a fixed project-scoped resident (locks/, trust/) — both names pass
//     harp validation, so only naming them keeps them out;
//   - a valid harp as a path component (harp.Validate, the same gate
//     paths.SessionStatePath applies when the path is BUILT); and
//   - recognisably an instance: either the index knows the harp, or the
//     directory carries the structural marker of an instance, its home/
//     subdirectory. Nothing else is recursed into, so a directory under state/
//     that this sweep does not understand — the RETIRED durable per-project
//     engine home, state/engines, is the concrete one — is left entirely
//     alone rather than being removed on the strength of its name.
//
// state/ ROOT FILES ARE NEVER CANDIDATES: dirty_tree_commit_ack.yaml is
// project-scoped local state whose loss is real, and harp.Validate accepts its
// name happily — only "is a directory" keeps it out.
//
// Best-effort per candidate: one failure warns and the sweep continues, so a
// single unremovable instance cannot cost the others their cleanup.
func ReapOrphanedSessionHomes(appPath string) (SessionHomeReapResult, error) {
	var result SessionHomeReapResult

	stateDir := paths.StatePath(appPath)
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if os.IsNotExist(err) {
			// A project that never ran a controlled-home session. Nothing to
			// do, and not a fault — deliberately decided BEFORE the index is
			// consulted, so an index fault cannot make a no-op sweep noisy.
			return result, nil
		}
		return result, fmt.Errorf("scan %q: %w", stateDir, err)
	}

	live, known, err := sessionLiveness()
	if err != nil {
		return SessionHomeReapResult{}, err
	}

	for _, e := range entries {
		name := e.Name()
		if !isSessionInstanceCandidate(e, stateDir, known) {
			continue
		}
		if live[name] {
			result.Skipped++
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(stateDir, name)); rerr != nil {
			clidiag.Warn("ctxloom", "session home reap: cannot remove the orphaned engine-home instance %q (it still holds a copied credential): %v",
				filepath.Join(stateDir, name), rerr)
			result.Skipped++
			continue
		}
		result.Reaped++
	}
	return result, nil
}

// sessionLiveness reads the session index once and returns two harp sets: the
// LIVE ones (an entry exists and its EndedAt is nil) and the KNOWN ones (an
// entry exists at all, ended or not). Known is what lets an index-recorded
// harp be a candidate without carrying the structural instance marker.
//
// An index that cannot be read is an error, never an empty pair — see
// ReapOrphanedSessionHomes' "no index, no sweep".
func sessionLiveness() (live, known map[string]bool, err error) {
	mgr, err := openSessions()
	if err != nil {
		return nil, nil, fmt.Errorf("open session index: %w", err)
	}
	idx, err := mgr.Load()
	if err != nil {
		return nil, nil, fmt.Errorf("read session index: %w", err)
	}
	live = make(map[string]bool, len(idx.Sessions))
	known = make(map[string]bool, len(idx.Sessions))
	for _, e := range idx.Sessions {
		known[e.HarpName] = true
		if e.EndedAt == nil {
			live[e.HarpName] = true
		}
	}
	return live, known, nil
}

// isSessionInstanceCandidate applies the allow-shape documented on
// ReapOrphanedSessionHomes. It answers "is this thing an engine-home instance
// at all", never "should it be removed" — liveness is the caller's decision.
func isSessionInstanceCandidate(e fs.DirEntry, stateDir string, known map[string]bool) bool {
	// A symlink's DirEntry type comes from lstat, so this is already false for
	// one; the explicit check states the rule the IsDir check only implies,
	// because "never follow a symlink" is the exclusion whose absence is
	// catastrophic rather than merely wrong.
	if e.Type()&fs.ModeSymlink != 0 {
		return false
	}
	// state/ root FILES — dirty_tree_commit_ack.yaml today — are project-scoped
	// local data, and their names pass harp validation. A file whose name IS an
	// index-known harp is the case where this gate is the only one left.
	if !e.IsDir() {
		return false
	}
	name := e.Name()
	// The FIXED project-scoped residents of state/. Enumerated deliberately,
	// the same way tests/arch's TestArch_LayoutHasNoHarpKeyedRows enumerates
	// them: a new fixed resident is added in both places on purpose, and a harp
	// can never be one.
	if name == paths.LocksDir || name == paths.TrustFileName {
		return false
	}
	if err := harpid.Validate(name); err != nil {
		return false
	}
	if known[name] {
		return true
	}
	// Unknown to the index: only the structural marker of an instance makes it
	// a candidate. Lstat, not Stat — a symlinked home/ must not vouch for a
	// directory whose contents live somewhere else entirely.
	info, err := os.Lstat(filepath.Join(stateDir, name, paths.SessionHomeDirName))
	return err == nil && info.IsDir()
}

// removeSessionInstance deletes harp's whole per-session state root under
// projectDir — the engine-home instance and any other session scratch beside
// it. Everything in it is copied or generated, so removal costs nothing; what
// it BUYS is that the credential copied in at instance time stops living in the
// project tree.
//
// Best-effort by contract: this runs on a session's exit path, where failing
// the exit would be worse than leaving bytes for the startup sweep
// (ReapOrphanedSessionHomes) to collect. It warns and returns.
func removeSessionInstance(projectDir, harp string) {
	if projectDir == "" || harp == "" {
		return
	}
	dir, err := paths.SessionStatePath(filepath.Join(projectDir, paths.AppDirName), harp)
	if err != nil {
		clidiag.Warn("ctxloom", "session end: cannot resolve %s's engine-home instance to remove it: %v", harp, err)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		clidiag.Warn("ctxloom", "session end: cannot remove %s's engine-home instance %q (it still holds a copied credential; the next startup sweep will retry): %v",
			harp, dir, err)
	}
}
