package isolation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
)

// worktreeReapTimeout bounds each candidate's git probing/removal during a
// startup sweep, mirroring worktreeTeardownTimeout — but per-candidate, so one
// wedged git call can't hang the whole sweep; the loop still moves on to the
// next candidate once a candidate's own context expires.
const worktreeReapTimeout = 30 * time.Second

// WorktreeVerdict is one candidate's outcome, in the reaper's established
// vocabulary (see WorktreeReapResult). It is a string type — not the old
// unexported int enum it replaces (worktreeReapOutcome) — so it can be
// rendered directly by a CLI listing without a lookup table.
type WorktreeVerdict string

const (
	// VerdictReapable: orphaned AND clean, as of classification. Removable;
	// ReapWorktrees has not yet acted on it (or a caller only classified).
	VerdictReapable WorktreeVerdict = "reapable"
	// VerdictReaped: removed by ReapWorktrees.
	VerdictReaped WorktreeVerdict = "reaped"
	// VerdictSpared: orphaned but carrying real (or unknowable) work — left
	// in place. Set either at classification time (the dead-owner tree was
	// already dirty) or by ReapWorktrees (the tree went dirty, or otherwise
	// became unsafe, between classification and removal — the TOCTOU case
	// teardownWorktree's own re-check catches).
	VerdictSpared WorktreeVerdict = "spared"
	// VerdictSkipped: the owning SESSION is alive or indeterminate (no lock
	// file, an untrusted filesystem), or the candidate's owning repo could
	// not be resolved. Never touched.
	VerdictSkipped WorktreeVerdict = "skipped"
)

// WorktreeCandidate is one ctxloom-owned scratch worktree and everything the
// reaper decided about it. Every field except Verdict/Reason is a plain READ
// FACT, gathered once by ClassifyOrphanedWorktrees and left untouched by
// ReapWorktrees (which only ever updates Verdict/Reason on the entries it
// acts on).
type WorktreeCandidate struct {
	// Path is the absolute checkout directory.
	Path string
	// Harp is the owning session's harp name, read off the path's own
	// <sessionsRoot>/<harp>/ephemeral/ctxloom-wt-* layout.
	Harp string
	// RepoDir is the owning repository, resolved via git.CommonDir; empty
	// when it could not be resolved (Verdict is then always Skipped).
	RepoDir string
	// OwnerPID is the pid read out of the owning SESSION's lock file, for a
	// human reading a listing, or 0 when there was none to read. It plays no
	// part in Owner and must not: see internal/shared/sessionlock.
	OwnerPID int
	// Owner is what the owning session's liveness lock said. Only
	// sessionlock.Dead permits removal; Alive and Indeterminate both refuse.
	Owner sessionlock.Verdict
	// Dirty reports whether unsafeToRemove judged the tree unsafe to remove
	// at classification time. Only ever computed for a confirmed-dead owner
	// (unsafeToRemove is never run against a live/indeterminate owner,
	// mirroring the pre-split reaper, which never got that far either).
	Dirty bool
	// Verdict is the decision: what ClassifyOrphanedWorktrees determined, or
	// — after ReapWorktrees ran — what actually happened.
	Verdict WorktreeVerdict
	// Reason is the human-readable "why" behind Verdict. Never empty for
	// VerdictSpared or VerdictSkipped.
	Reason string
}

// WorktreeReapResult tallies one ReapOrphanedWorktrees sweep, for a one-line
// boot-transcript summary: report only when something was actually removed, so
// the all-clear path stays silent.
type WorktreeReapResult struct {
	Reaped  int // orphaned AND clean — removed
	Spared  int // orphaned but carrying real (or unknowable) WIP — left in place
	Skipped int // owner alive, or indeterminate — left in place, untouched
}

// worktreeCandidatePrefix is the on-disk directory-name prefix
// worktreeScratchPath stamps every per-agent worktree checkout with
// (worktreeScratchPrefix + "-<sanitized-agent-id>-<rand>") — used here to pick
// worktree checkouts out of a session's ephemeral/ dir without matching the
// sibling toolchain-scratch dirs (which use their own "ctxloom-tmp-" prefix
// and are plain non-git scratch, out of this sweep's scope).
var worktreeCandidatePrefix = worktreeScratchPrefix + "-"

// ReapOrphanedWorktrees sweeps every per-session ephemeral dir under
// ~/.ctxloom/sessions for leftover per-agent worktree checkouts whose owning
// process is CONFIRMED dead, and removes the CLEAN ones via the exact same
// WIP-safe, nested-aware teardown() the graceful Cleanup path uses — never
// force.
//
// This is the fix for a bug: teardown() only ever runs on a graceful
// Cleanup(); nothing else ever runs it, so a crashed/killed run's worktree was
// orphaned PERMANENTLY — one leftover checkout (and one stale `git worktree
// list` registration in its project repo) per crash, forever. This sweep is
// the missing "something else": callers run it once at startup (see
// internal/cli's sweepOrphanedWorktrees, wired into `ctxloom run` / `ctxloom
// mcp`). It is best-effort throughout — a single candidate's failure warns and
// moves on, never aborting the sweep or the caller's own startup.
//
// Implementation note: this is Classify → Reap → tally and nothing else — see
// ClassifyOrphanedWorktrees and ReapWorktrees, which now carry the actual
// per-candidate rules that used to live in a single combined function
// (reapOneWorktree). This function's own signature and observable behaviour
// are unchanged by that split — worktree_reap_test.go pins it, and
// TestClassifyThenReap_MatchesReapOrphanedWorktrees additionally proves the
// two-step path produces identical tallies to this one on the same fixture.
// A resolve/scan failure still warns and returns a zero-value result rather
// than erroring: a startup sweep must never block or fail its caller.
//
// Every candidate gets exactly one of three outcomes, and the sweep is
// conservative on every one it cannot prove safe:
//
//   - The owning session's lock is HELD: SKIPPED — that session is running.
//   - No lock file, or a filesystem whose locks cannot be trusted: SKIPPED.
//     There is no way to prove the owner dead, and reaping a worktree a LIVE
//     agent still owns would repeat a known incident. Left for a human, or a
//     later explicitly-scoped sweep, to judge.
//   - The lock is FREE: the owning session is CONFIRMED gone, whether it
//     ended or crashed — the kernel drops the lock either way. teardown()
//     then makes the exact same WIP call it makes on the graceful path: real
//     (or unknowable) uncommitted work anywhere in the tree → SPARED, left in
//     place; genuinely clean → REAPED.
//
// LIVENESS IS PER-SESSION, which is what fixes the bug the per-worktree pid
// marker had: a FINISHED delegated agent's worktree recorded the pid of a
// still-running COORDINATOR, so it read as alive forever and nothing ever
// reclaimed it. The session lock answers about the session, so a finished
// session's leftovers are reclaimable however many agents ran under it.
func ReapOrphanedWorktrees(ctx context.Context, g git.Git) WorktreeReapResult {
	if g == nil {
		g = git.NewExec()
	}
	var result WorktreeReapResult

	candidates, err := ClassifyOrphanedWorktrees(ctx, g, "")
	if err != nil {
		// A startup sweep is fault-tolerant by contract (see doc above): warn
		// and report nothing rather than propagate. The error already names
		// what could not be resolved/scanned.
		clidiag.Warn("ctxloom", "worktree reap: %v", err)
		return result
	}

	for _, c := range ReapWorktrees(ctx, g, candidates) {
		switch c.Verdict {
		case VerdictReaped:
			result.Reaped++
		case VerdictSpared:
			result.Spared++
		default: // VerdictSkipped, and any candidate ReapWorktrees never touched
			result.Skipped++
		}
	}
	return result
}

// ClassifyOrphanedWorktrees inspects every ctxloom-owned scratch worktree
// under the sessions root and returns what the reaper WOULD do, mutating
// nothing on disk. harp != "" restricts the scan to that one harp's ephemeral
// dir; harp == "" scans every harp under the sessions root, matching
// ReapOrphanedWorktrees' historical scope.
//
// This is the read half reapOneWorktree never had: the old combined function
// tore a candidate down FIRST and inferred the verdict afterwards by stat-ing
// the directory, with the "why" going only to clidiag.Warn (never returned).
// A caller that wants to show its work before removing anything —
// `session worktrees`, the reason this function exists — needed a path that
// decides without acting.
func ClassifyOrphanedWorktrees(ctx context.Context, g git.Git, harp string) ([]WorktreeCandidate, error) {
	if g == nil {
		g = git.NewExec()
	}

	wtDirs, err := findOrphanCandidateDirs(harp)
	if err != nil {
		return nil, err
	}

	// One probe per HARP, not per checkout: liveness is a property of the
	// session that owns the ephemeral dir, so every worktree under one harp
	// shares a single verdict and probing per-checkout would only ask the
	// same question repeatedly.
	probes := make(map[string]sessionlock.Probe)
	candidates := make([]WorktreeCandidate, 0, len(wtDirs))
	for _, wtDir := range wtDirs {
		owner := harpOfWorktree(wtDir)
		probe, seen := probes[owner]
		if !seen {
			probe = sessionlock.Inspect(owner)
			probes[owner] = probe
		}
		candidates = append(candidates, classifyOneWorktree(ctx, g, wtDir, probe))
	}
	return candidates, nil
}

// ClassifyHarpWorktrees classifies one harp's scratch worktrees against an
// ALREADY-TAKEN liveness verdict, for a caller that is itself HOLDING that
// harp's session lock while it reclaims (operations.ReclaimAgedSessions).
//
// It exists because probing again from underneath our own hold would answer
// the wrong question: flock refuses a second descriptor on a file this
// process already locked, so sessionlock.Inspect would report the sweep's own
// lock as a LIVE owner and the sweep would skip every worktree it had just
// proven reclaimable. The caller passes the probe it already has.
func ClassifyHarpWorktrees(ctx context.Context, g git.Git, harp string, owner sessionlock.Probe) ([]WorktreeCandidate, error) {
	if g == nil {
		g = git.NewExec()
	}
	if harp == "" {
		return nil, fmt.Errorf("classify worktrees: a harp is required")
	}
	wtDirs, err := findOrphanCandidateDirs(harp)
	if err != nil {
		return nil, err
	}
	candidates := make([]WorktreeCandidate, 0, len(wtDirs))
	for _, wtDir := range wtDirs {
		candidates = append(candidates, classifyOneWorktree(ctx, g, wtDir, owner))
	}
	return candidates, nil
}

// harpOfWorktree reads the owning session's harp off a candidate's own path:
// <sessionsRoot>/<harp>/ephemeral/ctxloom-wt-*.
func harpOfWorktree(wtDir string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(wtDir)))
}

// findOrphanCandidateDirs resolves the set of "ctxloom-wt-*" directories to
// classify: every harp's ephemeral dir when harp is "", or just harp's own
// when it isn't. Errors here are real ones (an unresolvable sessions root, an
// unreadable — not merely absent — directory) meant to reach a CLI caller;
// ReapOrphanedWorktrees is the one caller that must swallow them instead.
func findOrphanCandidateDirs(harp string) ([]string, error) {
	if harp != "" {
		ephemeral, err := paths.HarpEphemeralDir(harp)
		if err != nil {
			return nil, fmt.Errorf("resolve %q's ephemeral dir: %w", harp, err)
		}
		dirs, err := readEphemeralWorktreeDirs(ephemeral)
		if err != nil {
			return nil, fmt.Errorf("scan %q: %w", ephemeral, err)
		}
		return dirs, nil
	}

	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		return nil, fmt.Errorf("resolve sessions dir: %w", err)
	}
	dirs, err := findEphemeralWorktrees(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("scan %q: %w", sessionsRoot, err)
	}
	return dirs, nil
}

// findEphemeralWorktrees returns every "ctxloom-wt-*" directory found directly
// under <sessionsRoot>/<harp>/ephemeral/, across every harp dir present. An
// absent sessionsRoot (nothing has ever run) is a quiet nil,nil — only a
// genuinely unreadable sessionsRoot itself is reported. A per-harp ephemeral
// dir that can't be read is silently skipped exactly as before the
// Classify/Reap split — readEphemeralWorktreeDirs' not-exist/error split only
// matters to the single-harp caller (findOrphanCandidateDirs), which needs to
// tell "no worktrees" apart from "cannot scan this harp".
func findEphemeralWorktrees(sessionsRoot string) ([]string, error) {
	harpDirs, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	for _, hd := range harpDirs {
		if !hd.IsDir() {
			continue
		}
		ephemeral := filepath.Join(sessionsRoot, hd.Name(), paths.EphemeralDirName)
		dirs, err := readEphemeralWorktreeDirs(ephemeral)
		if err != nil {
			continue // no ephemeral dir for this harp — nothing to sweep
		}
		out = append(out, dirs...)
	}
	return out, nil
}

// readEphemeralWorktreeDirs lists the "ctxloom-wt-*" directories directly
// under ephemeral. A missing ephemeral dir is nil,nil (that harp never
// provisioned a scratch worktree, not an error); any other read failure is
// returned so a single-harp caller can tell the two apart.
func readEphemeralWorktreeDirs(ephemeral string) ([]string, error) {
	entries, err := os.ReadDir(ephemeral)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), worktreeCandidatePrefix) {
			out = append(out, filepath.Join(ephemeral, e.Name()))
		}
	}
	return out, nil
}

// classifyOneWorktree decides a single candidate's verdict without touching
// disk, against the verdict its owning SESSION's liveness lock already
// returned: a live-or-unprovable owner is SKIPPED without ever probing git;
// only a CONFIRMED dead owner goes on to resolve the owning repo and run
// unsafeToRemove — the same gate teardownWorktree itself re-runs at removal
// time (see ReapWorktrees), so nothing here weakens the TOCTOU guard, it only
// PREVIEWS its outcome.
//
// THE LOCK ONLY EVER REFUSES, and Verdict.MayReclaim is the whole of the
// permission: Dead alone passes, so Alive, Indeterminate and any value that
// does not exist yet all fall out here rather than falling THROUGH toward a
// removal by omission. "Cannot determine" is never permission.
func classifyOneWorktree(parent context.Context, g git.Git, wtDir string, owner sessionlock.Probe) WorktreeCandidate {
	c := WorktreeCandidate{
		Path:     wtDir,
		Harp:     harpOfWorktree(wtDir),
		OwnerPID: owner.PID,
		Owner:    owner.Verdict,
	}

	if !owner.Verdict.MayReclaim() {
		c.Verdict = VerdictSkipped
		c.Reason = owner.Reason
		return c
	}

	ctx, cancel := context.WithTimeout(parent, worktreeReapTimeout)
	defer cancel()

	// Derive the owning repo from the worktree itself (git -C wtDir
	// rev-parse --git-common-dir, the same CommonDir seam
	// excludeConfigFromMerge already uses) rather than requiring the caller to
	// somehow already know it — an orphan's whole problem is that nothing live
	// remembers where it came from. A non-bare repo's common dir always ends
	// in "<repo>/.git", so its parent is the repo root WorktreeRemove/
	// WorktreeList expect as repoDir.
	common, err := g.CommonDir(ctx, wtDir)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree reap: cannot resolve %q's owning repo (leaving it in place): %v", wtDir, err)
		c.Verdict = VerdictSkipped
		c.Reason = fmt.Sprintf("its owning repository could not be resolved: %v", err)
		return c
	}
	c.RepoDir = filepath.Dir(common)

	if unsafe, reason := unsafeToRemove(ctx, g, wtDir); unsafe {
		c.Dirty = true
		c.Verdict = VerdictSpared
		c.Reason = fmt.Sprintf("session %s has ended, but the worktree %s", c.Harp, reason)
		return c
	}

	c.Verdict = VerdictReapable
	return c
}

// ReapWorktrees removes exactly those candidates whose Verdict is
// VerdictReapable, RE-CHECKING each one's safety at removal time via
// teardownWorktree (unchanged, and never force) — a tree that went dirty
// between ClassifyOrphanedWorktrees and this call is spared, not removed,
// preserving the exact TOCTOU guard the pre-split reaper had. It returns a
// COPY of candidates with Verdict/Reason updated to the outcome that actually
// occurred; candidates it did not act on (already Spared or Skipped by
// Classify) are returned unchanged.
//
// THERE IS NO PER-WORKTREE OWNER MARKER TO CLEAN UP: liveness is the owning
// SESSION's lock, which lives beside the harp directory and outlives any one
// checkout. A spared tree therefore keeps its full dead-owner reason on the
// next classification instead of decaying to "no marker — indeterminate",
// which is what the per-worktree marker did whenever a tree survived.
func ReapWorktrees(ctx context.Context, g git.Git, candidates []WorktreeCandidate) []WorktreeCandidate {
	if g == nil {
		g = git.NewExec()
	}
	out := make([]WorktreeCandidate, len(candidates))
	copy(out, candidates)

	for i := range out {
		if out[i].Verdict != VerdictReapable {
			continue
		}
		c := &out[i]

		removeCtx, cancel := context.WithTimeout(ctx, worktreeReapTimeout)
		// Reuse the EXACT WIP-safe, nested-worktree-aware removal the
		// graceful path uses — never a bespoke/looser check, and never
		// force. It warns (clidiag) and leaves the tree in place on any
		// doubt, so the only thing left to do here is tell REAPED apart
		// from SPARED by checking whether it's actually gone afterward.
		teardownWorktree(removeCtx, g, c.RepoDir, c.Path)
		cancel()

		if worktreeRemoved(c.Path) {
			c.Verdict = VerdictReaped
			c.Reason = ""
		} else {
			c.Verdict = VerdictSpared
			c.Reason = "went dirty, or otherwise became unsafe, between listing and removal; teardown left it in place"
		}
	}
	return out
}

// worktreeRemoved reports whether teardown actually removed wtDir. ONLY
// ErrNotExist proves removal: any other stat failure — EACCES on a parent,
// ELOOP, ENAMETOOLONG — means the tree's fate is unknown, and the sweep must
// never report cleanup it cannot see. Unknown is therefore reported as
// NOT-removed (SPARED, the conservative half of the sweep's own contract) and
// warned about, since an unreadable ephemeral path is itself a fault worth
// surfacing.
func worktreeRemoved(wtDir string) bool {
	_, err := os.Stat(wtDir)
	switch {
	case err == nil:
		return false
	case errors.Is(err, fs.ErrNotExist):
		return true
	default:
		clidiag.Warn("ctxloom", "worktree reap: cannot tell whether %q was removed (%v); reporting it as spared", wtDir, err)
		return false
	}
}
