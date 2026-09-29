package isolation

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// deadPid is a pid guaranteed not to name a live process: it exceeds even a
// kernel.pid_max=4194304 configuration by a wide margin, so pidAlive(deadPid)
// is deterministically false with no fork/kill/race required — the standard
// way to manufacture a "confirmed dead owner" in a test.
const deadPid = 999999999

// seedDeadSession makes harp read sessionlock.Dead: its lock file exists and
// is FREE, which is exactly the state the kernel leaves behind once the
// owning process has ended — gracefully or by SIGKILL, the lock drops either
// way. Hold-then-Release is used rather than hand-writing the file so the
// fixture cannot drift from the real on-disk shape.
func seedDeadSession(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	sessionlock.Release(harp)
}

// seedLiveSession makes harp read sessionlock.Alive: this very test process
// holds the lock for the rest of the test, so the sweep probing it finds a
// genuinely held lock rather than a simulated one.
func seedLiveSession(t *testing.T, harp string) {
	t.Helper()
	require.NoError(t, sessionlock.Hold(harp))
	t.Cleanup(func() { sessionlock.Release(harp) })
}

// TestReapOrphanedWorktrees_ReapsCleanOrphan is the red-first proof for
// a bug: a worktree whose owning process crashed (never reaching
// its own Cleanup) is NEVER swept by anything today. This proves the missing
// half exists and works — a startup sweep finds the ephemeral checkout,
// confirms its recorded owner pid is dead, confirms the tree is genuinely
// clean, and removes it exactly as the graceful path would.
func TestReapOrphanedWorktrees_ReapsCleanOrphan(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the startup-reaper integration test")
	}
	testsupport.Isolate(t)
	repo := initRealRepo(t)

	pol := NewWorktree(git.NewExec())
	pol.state = SessionState{Harp: "reap-clean-harp"}
	ctx := context.Background()
	ws, err := pol.prepareWorkspace(ctx, repo, "member-crashed")
	require.NoError(t, err)
	wtDir := ws.Dir()
	t.Cleanup(func() { _ = os.RemoveAll(wtDir) }) // safety net if the assertion below fails first

	// Simulate the crash: the owning SESSION is gone, WITHOUT ever calling
	// ws.Cleanup() — exactly what a killed/crashed run leaves behind today.
	seedDeadSession(t, "reap-clean-harp")

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Reaped: 1}, result, "one clean orphan, confirmed dead owner, reaped")

	assert.NoDirExists(t, wtDir, "the orphaned checkout is removed")

	out := gitRun(t, repo, "worktree", "list", "--porcelain")
	assert.NotContains(t, out, wtDir, "no leftover worktree registration after the sweep")
}

// TestReapOrphanedWorktrees_SparesUncommittedWIP proves the sweep is exactly
// as WIP-safe as the graceful path: a confirmed-dead owner is NOT enough to
// remove a worktree carrying real uncommitted work — teardown's own dirty
// check still gates the actual removal, so the sweep only ever clears
// GENUINELY clean orphans (the worktree-WIP-safety discipline: never destroy
// uncommitted work).
func TestReapOrphanedWorktrees_SparesUncommittedWIP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the startup-reaper integration test")
	}
	testsupport.Isolate(t)
	repo := initRealRepo(t)

	pol := NewWorktree(git.NewExec())
	pol.state = SessionState{Harp: "reap-wip-harp"}
	ctx := context.Background()
	ws, err := pol.prepareWorkspace(ctx, repo, "member-crashed-wip")
	require.NoError(t, err)
	wtDir := ws.Dir()
	t.Cleanup(func() {
		// Manual force-cleanup: this test's whole point is proving the tree
		// SURVIVES the sweep, so a raw RemoveAll plus a real
		// `worktree remove --force` keeps git's own bookkeeping tidy without
		// going through git.WorktreeRemove at all — that seam never force-
		// removes (no force parameter exists on it).
		_ = os.RemoveAll(wtDir)
		_ = gitRunNoFail(repo, "worktree", "remove", "--force", wtDir)
	})

	// Real, uncommitted WIP a developer member would have left behind.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "wip.txt"), []byte("uncommitted work"), 0o644))

	seedDeadSession(t, "reap-wip-harp")

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Spared: 1}, result, "confirmed-dead owner, but dirty tree — spared, not reaped")

	assert.DirExists(t, wtDir, "the worktree survives: it carries uncommitted work")
	assert.FileExists(t, filepath.Join(wtDir, "wip.txt"), "the uncommitted work itself is untouched")
}

// TestReapOrphanedWorktrees_SkipsLiveOwner proves the other half of the
// safety contract: even a genuinely clean worktree is left alone when its
// recorded owner is still alive — the sweep never reaps out from under a
// running agent (the exact incident this design explicitly guards against).
func TestReapOrphanedWorktrees_SkipsLiveOwner(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the startup-reaper integration test")
	}
	testsupport.Isolate(t)
	repo := initRealRepo(t)

	pol := NewWorktree(git.NewExec())
	pol.state = SessionState{Harp: "reap-live-harp"}
	ctx := context.Background()
	ws, err := pol.prepareWorkspace(ctx, repo, "member-live")
	require.NoError(t, err)
	wtDir := ws.Dir()
	t.Cleanup(func() { require.NoError(t, ws.Cleanup()) })

	// The owning session is running: this test process holds its lock.
	seedLiveSession(t, "reap-live-harp")

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Skipped: 1}, result, "owner alive — untouched")
	assert.DirExists(t, wtDir, "a live-owned worktree is never touched by the sweep")
}

// TestReapOrphanedWorktrees_SkipsIndeterminateOwner proves the conservative
// default for the case the lock cannot establish: a "ctxloom-wt-*" directory
// whose owning session has NO lock file at all (a session from before the
// lock existed, or one whose Hold failed) is left strictly alone rather than
// assumed safe — "can't prove dead" is treated the same as "alive", never as
// "reapable". A missing lock must never read as a free one.
func TestReapOrphanedWorktrees_SkipsIndeterminateOwner(t *testing.T) {
	home := testsupport.Isolate(t)
	ctx := context.Background()

	ephemeral := filepath.Join(home, ".ctxloom", "sessions", "reap-legacy-harp", "ephemeral")
	require.NoError(t, os.MkdirAll(ephemeral, 0o755))
	wtDir := filepath.Join(ephemeral, "ctxloom-wt-legacy-orphan-abc123")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	// Deliberately NO session lock file written.

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Skipped: 1}, result, "no session lock — indeterminate, never touched")
	assert.DirExists(t, wtDir, "a lock-less candidate is left exactly as found")
}

// gitRunNoFail runs a git command in dir, discarding any error — used only in
// t.Cleanup callbacks where the test has already made its assertions and a
// tidy-up failure must not mask them.
func gitRunNoFail(dir string, args ...string) error {
	return gitCmd(dir, args...).Run()
}

// A reaper that could not act on ANY candidate is NOT silent, and that is the
// whole point: a clean sweep says nothing, so every line the sweep does print
// is a candidate it declined to remove and why. This pins the diagnostic
// surface itself — the register claim that the failure surface is "discarded
// entirely" rests on WorktreeReapResult carrying no error count, but the
// counts were never the reporting channel.
//
// Driven through the CommonDir failure because it is the one arm that can be
// forced without a real repo: a candidate whose owning repo cannot be
// resolved is left in place, forever, and the user has to be told which one.
func TestReapOrphanedWorktrees_UnresolvableCandidateIsReportedNotSwallowed(t *testing.T) {
	testsupport.Isolate(t)

	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	ephemeral := filepath.Join(sessionsRoot, "reap-warn-harp", "ephemeral")
	require.NoError(t, os.MkdirAll(ephemeral, 0o755))
	// A candidate that matches the sweep's prefix but is not a git worktree at
	// all, so CommonDir fails on it.
	wtDir := filepath.Join(ephemeral, worktreeCandidatePrefix+"orphan-abc")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	seedDeadSession(t, "reap-warn-harp")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	result := ReapOrphanedWorktrees(context.Background(), git.NewExec())

	assert.Equal(t, WorktreeReapResult{Skipped: 1}, result)
	out := sink.String()
	assert.Contains(t, out, strconv.Quote(wtDir), "the candidate the sweep could not act on must be named, quoted")
	assert.Contains(t, out, "leaving it in place",
		"and the sweep must say what it did about it")
}

// The contrast that makes the line above informative: a sweep with nothing to
// do prints nothing at all, so any output IS the failure report.
func TestReapOrphanedWorktrees_CleanSweepIsSilent(t *testing.T) {
	testsupport.Isolate(t)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	result := ReapOrphanedWorktrees(context.Background(), git.NewExec())

	assert.Equal(t, WorktreeReapResult{}, result)
	assert.Empty(t, sink.String(), "a clean sweep is silent — that is what makes a warning a signal")
}

// buildFourVerdictFixture plants one real repo with four scratch worktrees
// covering all four verdicts — clean+dead, dirty+dead, no-lock, and live —
// the same four populations J001300's own acceptance fixture builds
// (tests/acceptance/steps_j001300_closeout.go), so this test's fixture is not
// a narrower stand-in. Returns the four checkout dirs so the caller can
// register cleanup.
//
// ONE SESSION PER OWNER-STATE, and that is forced rather than stylistic:
// liveness is a property of the SESSION, so a single harp can no longer hold
// a dead-owned and a live-owned worktree at once. The dead harp carries both
// dead-owner trees, because clean-vs-dirty is a property of the CHECKOUT and
// still varies freely within one session.
func buildFourVerdictFixture(t *testing.T) (repo string, dirs []string) {
	t.Helper()
	testsupport.Isolate(t)
	repo = initRealRepo(t)

	ctx := context.Background()
	prepare := func(harp, member string) workspace {
		pol := NewWorktree(git.NewExec())
		pol.state = SessionState{Harp: harp}
		ws, err := pol.prepareWorkspace(ctx, repo, member)
		require.NoError(t, err)
		return ws
	}

	// An ended session, carrying one clean tree (reapable) and one holding
	// uncommitted work (spared).
	clean := prepare("equiv-dead-harp", "member-clean")
	wip := prepare("equiv-dead-harp", "member-wip")
	require.NoError(t, os.WriteFile(filepath.Join(wip.Dir(), "wip.txt"), []byte("uncommitted"), 0o644))
	seedDeadSession(t, "equiv-dead-harp")

	// A session with no lock file at all: unprovable, never touched.
	unknowable := prepare("equiv-unknowable-harp", "member-unknowable")

	// A session this very process is holding the lock for: alive.
	live := prepare("equiv-live-harp", "member-live")
	seedLiveSession(t, "equiv-live-harp")

	dirs = []string{clean.Dir(), wip.Dir(), unknowable.Dir(), live.Dir()}
	t.Cleanup(func() {
		for _, d := range dirs {
			_ = os.RemoveAll(d)
			_ = gitRunNoFail(repo, "worktree", "remove", "--force", d)
		}
	})
	return repo, dirs
}

// TestClassifyThenReap_MatchesReapOrphanedWorktrees is the required proof for
// the reapOneWorktree split, not merely an assertion of it: "behaviour-
// preserving by construction" (teardownWorktree/unsafeToRemove untouched,
// ReapOrphanedWorktrees keeps its signature) is a CLAIM. This measures it —
// two IDENTICAL fixtures (all four verdicts represented at once: reaped,
// spared, skipped-alive, skipped-indeterminate), one driven through the
// original combined entrypoint and one through the new Classify-then-Reap
// two-step, must tally the same.
func TestClassifyThenReap_MatchesReapOrphanedWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the startup-reaper integration test")
	}

	var combined, split WorktreeReapResult

	t.Run("combined ReapOrphanedWorktrees", func(t *testing.T) {
		buildFourVerdictFixture(t)
		combined = ReapOrphanedWorktrees(context.Background(), git.NewExec())
	})

	t.Run("split Classify then Reap", func(t *testing.T) {
		buildFourVerdictFixture(t)
		candidates, err := ClassifyOrphanedWorktrees(context.Background(), git.NewExec(), "")
		require.NoError(t, err)
		reaped := ReapWorktrees(context.Background(), git.NewExec(), candidates)
		for _, c := range reaped {
			switch c.Verdict {
			case VerdictReaped:
				split.Reaped++
			case VerdictSpared:
				split.Spared++
			case VerdictSkipped:
				split.Skipped++
			default:
				t.Fatalf("candidate %s left with verdict %q — ReapWorktrees must resolve every reapable "+
					"candidate to reaped or spared, and leave every other candidate at the verdict Classify gave it",
					c.Path, c.Verdict)
			}
		}
	})

	// Sanity: the fixture must actually exercise all four verdicts, or a
	// tally match below would prove nothing.
	require.Equal(t, WorktreeReapResult{Reaped: 1, Spared: 1, Skipped: 2}, combined,
		"fixture sanity: one reaped (clean/dead), one spared (dirty/dead), two skipped (no-lock, live)")

	assert.Equal(t, combined, split,
		"Classify->Reap must tally IDENTICALLY to the combined ReapOrphanedWorktrees on the same fixture")
}
