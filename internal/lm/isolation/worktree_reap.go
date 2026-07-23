package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
// Called from both the graceful Cleanup path and the startup reaper so a
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

// worktreeReapOutcome classifies what ReapOrphanedWorktrees did with one
// candidate.
type worktreeReapOutcome int

const (
	// worktreeReapSkipped: left untouched — the owner is alive, or its
	// liveness could not be proven (no/unreadable marker), or the candidate's
	// owning repo could not be resolved. Indistinguishable from "still in use"
	// by design: an unprovable case is never treated as safe to remove.
	worktreeReapSkipped worktreeReapOutcome = iota
	// worktreeReapSpared: the owner is CONFIRMED dead, but the worktree (or a
	// worktree nested under it) carries uncommitted work or an unknowable git
	// state — teardown's own WIP guard left it in place, exactly as it would
	// on the graceful path.
	worktreeReapSpared
	// worktreeReaped: the owner is confirmed dead and the tree was clean —
	// teardown removed it.
	worktreeReaped
)

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
// This is bony-carry bug #2's fix: teardown() only ever runs on a graceful
// Cleanup(); nothing else ever runs it, so a crashed/killed run's worktree was
// orphaned PERMANENTLY — one leftover checkout (and one stale `git worktree
// list` registration in its project repo) per crash, forever. This sweep is
// the missing "something else": callers run it once at startup (see
// internal/cli's sweepOrphanedWorktrees, wired into `ctxloom run` / `ctxloom
// mcp`). It is best-effort throughout — a single candidate's failure warns and
// moves on, never aborting the sweep or the caller's own startup.
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

	sessionsRoot, err := paths.HomeSessionsDir()
	if err != nil {
		clidiag.Warn("ctxloom", "worktree reap: cannot resolve sessions dir: %v", err)
		return result
	}
	candidates, err := findEphemeralWorktrees(sessionsRoot)
	if err != nil {
		clidiag.Warn("ctxloom", "worktree reap: cannot scan %q: %v", sessionsRoot, err)
		return result
	}

	for _, wtDir := range candidates {
		switch reapOneWorktree(ctx, g, wtDir) {
		case worktreeReaped:
			result.Reaped++
		case worktreeReapSpared:
			result.Spared++
		default:
			result.Skipped++
		}
	}
	return result
}

// findEphemeralWorktrees returns every "ctxloom-wt-*" directory found directly
// under <sessionsRoot>/<harp>/ephemeral/, across every harp dir present. An
// absent sessionsRoot (nothing has ever run) or an absent/unreadable per-harp
// ephemeral dir (that harp never provisioned a worktree) is a quiet skip, not
// an error — only a genuinely unreadable sessionsRoot itself is reported.
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
		entries, err := os.ReadDir(ephemeral)
		if err != nil {
			continue // no ephemeral dir for this harp — nothing to sweep
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), worktreeCandidatePrefix) {
				out = append(out, filepath.Join(ephemeral, e.Name()))
			}
		}
	}
	return out, nil
}

// reapOneWorktree classifies and (when safe) removes a single candidate. See
// ReapOrphanedWorktrees' doc for the three-outcome contract.
func reapOneWorktree(parent context.Context, g git.Git, wtDir string) worktreeReapOutcome {
	pid, ok := readWorktreeOwner(wtDir)
	if !ok {
		return worktreeReapSkipped // indeterminate owner — never touch
	}
	if pidAlive(pid) {
		return worktreeReapSkipped // live owner
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
		return worktreeReapSkipped
	}
	repoDir := filepath.Dir(common)

	// Reuse the EXACT WIP-safe, nested-worktree-aware removal the graceful
	// path uses — never a bespoke/looser check, and never force. It warns
	// (clidiag) and leaves wtDir in place on any doubt, so the only thing left
	// to do here is tell REAPED apart from SPARED by checking whether it's
	// actually gone afterward.
	ws := &worktreeWorkspace{git: g, repoDir: repoDir, dir: wtDir}
	ws.teardown(ctx, wtDir)
	removeWorktreeOwnerMarker(wtDir)

	if _, statErr := os.Stat(wtDir); statErr != nil {
		return worktreeReaped
	}
	return worktreeReapSpared
}
