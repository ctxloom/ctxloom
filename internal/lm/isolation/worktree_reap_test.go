package isolation

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadPid is a pid guaranteed not to name a live process: it exceeds even a
// kernel.pid_max=4194304 configuration by a wide margin, so pidAlive(deadPid)
// is deterministically false with no fork/kill/race required — the standard
// way to manufacture a "confirmed dead owner" in a test.
const deadPid = 999999999

// setWorktreeOwnerForTest overwrites wtDir's sibling owner marker (see
// recordWorktreeOwner) directly, standing in for "this worktree's owning
// process is now pid" without actually needing to spawn/kill one.
func setWorktreeOwnerForTest(t *testing.T, wtDir string, pid int) {
	t.Helper()
	require.NoError(t, os.WriteFile(wtDir+worktreeOwnerSuffix, fmt.Appendf(nil, "%d\n", pid), 0o600))
}

// TestReapOrphanedWorktrees_ReapsCleanOrphan is the red-first proof for
// bony-carry bug #2: a worktree whose owning process crashed (never reaching
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

	pol := NewWorktree(git.NewExec(), "")
	pol.state = SessionState{Harp: "reap-clean-harp"}
	ctx := context.Background()
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-crashed")
	require.NoError(t, err)
	wtDir := ws.Dir()
	t.Cleanup(func() { _ = os.RemoveAll(wtDir) }) // safety net if the assertion below fails first

	require.FileExists(t, wtDir+worktreeOwnerSuffix, "PrepareWorkspace stamps an owner marker")
	// Simulate the crash: the owning process is gone, WITHOUT ever calling
	// ws.Cleanup() — exactly what a killed/crashed run leaves behind today.
	setWorktreeOwnerForTest(t, wtDir, deadPid)

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Reaped: 1}, result, "one clean orphan, confirmed dead owner, reaped")

	assert.NoDirExists(t, wtDir, "the orphaned checkout is removed")
	assert.NoFileExists(t, wtDir+worktreeOwnerSuffix, "the owner marker is removed alongside it")

	out := gitOut(t, repo, "worktree", "list", "--porcelain")
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

	pol := NewWorktree(git.NewExec(), "")
	pol.state = SessionState{Harp: "reap-wip-harp"}
	ctx := context.Background()
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-crashed-wip")
	require.NoError(t, err)
	wtDir := ws.Dir()
	t.Cleanup(func() {
		// Manual force-cleanup: this test's whole point is proving the tree
		// SURVIVES the sweep, so a raw RemoveAll (never the production
		// force-remove path) plus a real `worktree remove --force` keeps git's
		// own bookkeeping tidy without touching WorktreeRemove's force=false
		// contract.
		_ = os.RemoveAll(wtDir)
		_ = gitRunNoFail(repo, "worktree", "remove", "--force", wtDir)
	})

	// Real, uncommitted WIP a developer member would have left behind.
	require.NoError(t, os.WriteFile(filepath.Join(wtDir, "wip.txt"), []byte("uncommitted work"), 0o644))

	setWorktreeOwnerForTest(t, wtDir, deadPid)

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

	pol := NewWorktree(git.NewExec(), "")
	pol.state = SessionState{Harp: "reap-live-harp"}
	ctx := context.Background()
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-live")
	require.NoError(t, err)
	wtDir := ws.Dir()
	t.Cleanup(func() { require.NoError(t, ws.Cleanup()) })

	// PrepareWorkspace already stamped THIS test process's own (very much
	// alive) pid as the owner — no override needed.

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Skipped: 1}, result, "owner alive — untouched")
	assert.DirExists(t, wtDir, "a live-owned worktree is never touched by the sweep")
}

// TestReapOrphanedWorktrees_SkipsIndeterminateOwner proves the conservative
// default for the case a marker can't establish: a "ctxloom-wt-*" directory
// with NO owner marker at all (a legacy orphan from before this fix, or a
// crash between WorktreeAdd and the marker write) is left strictly alone
// rather than assumed safe — "can't prove dead" is treated the same as
// "alive", never as "reapable".
func TestReapOrphanedWorktrees_SkipsIndeterminateOwner(t *testing.T) {
	home := testsupport.Isolate(t)
	ctx := context.Background()

	ephemeral := filepath.Join(home, ".ctxloom", "sessions", "reap-legacy-harp", "ephemeral")
	require.NoError(t, os.MkdirAll(ephemeral, 0o755))
	wtDir := filepath.Join(ephemeral, "ctxloom-wt-legacy-orphan-abc123")
	require.NoError(t, os.MkdirAll(wtDir, 0o755))
	// Deliberately NO owner marker written.

	result := ReapOrphanedWorktrees(ctx, git.NewExec())
	assert.Equal(t, WorktreeReapResult{Skipped: 1}, result, "no owner marker — indeterminate, never touched")
	assert.DirExists(t, wtDir, "a marker-less candidate is left exactly as found")
}

// gitRunNoFail runs a git command in dir, discarding any error — used only in
// t.Cleanup callbacks where the test has already made its assertions and a
// tidy-up failure must not mask them.
func gitRunNoFail(dir string, args ...string) error {
	return gitCmd(dir, args...).Run()
}
