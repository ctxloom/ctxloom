package isolation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/pidalive"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// worktreeOwnerSuffix names the sibling pid-marker file recordWorktreeOwner
// writes NEXT TO each ephemeral worktree checkout — deliberately outside the
// checkout itself (a sibling of the "ctxloom-wt-*" dir, not a file inside it),
// so it can never appear in the worktree's own `git status` and can never
// interact with the config-exclusion machinery (skipTrackedConfig,
// excludeConfigFromMerge). ReapOrphanedWorktrees is the only reader.
const worktreeOwnerSuffix = ".owner.pid"

// worktreeReapTimeout bounds each candidate's git probing/removal during a
// startup sweep, mirroring worktreeTeardownTimeout — but per-candidate, so one
// wedged git call can't hang the whole sweep; the loop still moves on to the
// next candidate once a candidate's own context expires.
const worktreeReapTimeout = 30 * time.Second

// recordWorktreeOwner stamps wtDir's sibling owner marker with this process's
// pid (see worktreeOwnerSuffix). Best-effort: a failed write only means a
// future startup sweep can never prove this worktree's owner is dead, so it
// will be conservatively SKIPPED forever rather than force-reaped — it never
// blocks or fails PrepareWorkspace itself.
func recordWorktreeOwner(wtDir string) {
	marker := wtDir + worktreeOwnerSuffix
	if err := os.WriteFile(marker, fmt.Appendf(nil, "%d\n", os.Getpid()), 0o600); err != nil {
		clidiag.Warn("ctxloom", "worktree: cannot record owner pid for %q (a crashed run would leave this un-reapable at startup): %v", wtDir, err)
	}
}

// removeWorktreeOwnerMarker best-effort removes wtDir's sibling owner marker.
// Called from both the graceful Cleanup path and ReapWorktrees (only once a
// candidate is actually confirmed removed — see ReapWorktrees' doc) so a
// marker never outlives the checkout it describes; a missing file is a
// silent no-op (os.Remove's ErrNotExist is expected and uninteresting here).
func removeWorktreeOwnerMarker(wtDir string) {
	_ = os.Remove(wtDir + worktreeOwnerSuffix)
}

// readWorktreeOwner reads and parses wtDir's sibling owner marker, reporting
// ok=false on ANY doubt (missing file, unreadable, unparsable, non-positive) —
// the reaper treats "can't prove who owned this" identically to "still owned
// by someone alive": never touch it.
func readWorktreeOwner(wtDir string) (pid int, ok bool) {
	raw, err := os.ReadFile(wtDir + worktreeOwnerSuffix)
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

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
	// VerdictSkipped: owner alive, owner indeterminate (no/unreadable
	// marker), or the candidate's owning repo could not be resolved. Never
	// touched.
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
	// OwnerPID is the pid recorded in the candidate's sibling owner marker,
	// or 0 when no marker was found (or it was unreadable/unparsable).
	OwnerPID int
	// OwnerState is what pidalive.Probe said about OwnerPID. Meaningless
	// (zero value, Dead) when OwnerPID is 0 — check OwnerPID first.
	OwnerState pidalive.State
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
// boot-transcript summary (mirrors the CLI's purgeLegacyBundles reporting
// shape).
type WorktreeReapResult struct {
	Reaped  int // orphaned AND clean — removed
	Spared  int // orphaned but carrying real (or unknowable) WIP — left in place
	Skipped int // owner alive, or indeterminate — left in place, untouched
}

// worktreeCandidatePrefix is the on-disk directory-name prefix
// worktreeScratchPath stamps every per-agent worktree checkout with
// (worktreeScratchPrefix + "-<sanitized-agent-id>-<rand>") — used here to pick
// worktree checkouts out of a session's ephemeral/ dir without matching its
// sibling config-home/curated-home/toolchain-scratch dirs (which use their own
// "ctxloom-cfg-"/"ctxloom-home-"/"ctxloom-tmp-" prefixes and are plain
// non-git scratch, out of this sweep's scope).
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
//   - No owner marker, or an unreadable/unparsable one: SKIPPED. This is
//     EITHER a worktree from before ctxloom could record an owner at all, OR
//     one that crashed between WorktreeAdd and the marker write — either way
//     there is no way to prove the owner is dead, and reaping a worktree a
//     LIVE agent still owns would repeat a known incident (a startup reaper
//     must be certain the owner is dead before it touches anything). Left for
//     a human, or a later explicitly-scoped sweep, to judge.
//   - The marker's pid is alive: SKIPPED — a live process still owns it.
//   - The marker's pid is dead: the owner is CONFIRMED gone. teardown() then
//     makes the exact same WIP call it makes on the graceful path: real (or
//     unknowable) uncommitted work anywhere in the tree → SPARED, left in
//     place; genuinely clean → REAPED.
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

	candidates := make([]WorktreeCandidate, 0, len(wtDirs))
	for _, wtDir := range wtDirs {
		candidates = append(candidates, classifyOneWorktree(ctx, g, wtDir))
	}
	return candidates, nil
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
// disk, applying exactly the rules reapOneWorktree used to apply only AFTER
// acting: no/unreadable owner marker or a live-or-unconfirmable owner is
// SKIPPED without ever probing git; only a CONFIRMED dead owner goes on to
// resolve the owning repo and run unsafeToRemove — the same gate
// teardownWorktree itself re-runs at removal time (see ReapWorktrees), so
// nothing here weakens the TOCTOU guard, it only PREVIEWS its outcome.
func classifyOneWorktree(parent context.Context, g git.Git, wtDir string) WorktreeCandidate {
	c := WorktreeCandidate{
		Path: wtDir,
		Harp: filepath.Base(filepath.Dir(filepath.Dir(wtDir))),
	}

	pid, ok := readWorktreeOwner(wtDir)
	if !ok {
		c.Verdict = VerdictSkipped
		c.Reason = "no owner marker recorded — the owner cannot be proven dead, so it is treated exactly like a live one"
		return c
	}
	c.OwnerPID = pid
	c.OwnerState = pidalive.Probe(pid)
	// MaybeAlive (not a bare == Alive check) treats an unconfirmable probe
	// the same as a live owner — reaping is a DESTRUCTIVE, irreversible
	// decision, so an unsure verdict must skip exactly like a confirmed-live
	// owner does, never fall through toward removal.
	if c.OwnerState.MaybeAlive() {
		c.Verdict = VerdictSkipped
		if c.OwnerState == pidalive.Alive {
			c.Reason = fmt.Sprintf("owner process %d is alive", pid)
		} else {
			c.Reason = fmt.Sprintf("owner process %d's liveness could not be confirmed", pid)
		}
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
		c.Reason = fmt.Sprintf("owner process %d is confirmed dead, but the worktree %s", pid, reason)
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
// The owner marker is removed ONLY for a candidate that is actually confirmed
// removed (VerdictReaped) — deliberately narrower than the pre-split
// reapOneWorktree, which called removeWorktreeOwnerMarker unconditionally
// right after teardownWorktree, before checking whether anything was even
// removed. That was a real defect, not a design choice: a tree that survived
// as SPARED (dirty at removal time) lost its own owner marker anyway, so the
// NEXT classification of that same tree would read it as "no marker —
// indeterminate" and silently downgrade it from Spared to Skipped, losing the
// dead-owner reason it had already proven. Fixing it changes one thing an
// external caller COULD observe (the marker file's survival on a spared
// candidate) but not WorktreeReapResult's tallies — see
// TestClassifyThenReap_MatchesReapOrphanedWorktrees, which proves the tallies
// are unaffected on a fixture built to exercise all four verdicts at once.
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
			removeWorktreeOwnerMarker(c.Path)
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
