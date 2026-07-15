package isolation

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorktreePolicy_RealGitLifecycle is the P1 integration gate against a REAL
// git repo (default exec Git): the policy adds a worktree, we run a no-op in it,
// then the WIP-safe teardown removes it with no leftover in `git worktree list`.
// Also asserts the config-out-of-merge excludes land in the common-dir
// info/exclude (§3.1). Skips cleanly when git is unavailable so the normal suite
// stays green.
func TestWorktreePolicy_RealGitLifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the worktree policy integration test")
	}
	ctx := context.Background()
	repo := initRealRepo(t)

	pol := NewWorktree(git.NewExec(), "")
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-int")
	require.NoError(t, err, "PrepareWorkspace must add a worktree in a real repo")
	wtDir := ws.Dir()
	// Safety net: the test below calls ws.Cleanup() itself and asserts the dir
	// is gone, but that call is plain sequential code, not a defer/t.Cleanup —
	// any earlier require/assert failure or panic in a later mutation-testing
	// run would skip it and leak this checkout under the OS temp dir. Register
	// the removal now so it always runs regardless of how the test exits.
	// os.RemoveAll on an already-removed dir (the normal, asserted path) is a
	// harmless no-op.
	t.Cleanup(func() { _ = os.RemoveAll(wtDir) })
	cleanupConfigHome(t, ws)

	// The worktree exists, is under the OS temp dir, and carries the seed file.
	info, err := os.Stat(ws.Dir())
	require.NoError(t, err)
	require.True(t, info.IsDir(), "the worktree dir exists")
	assert.True(t, strings.HasPrefix(ws.Dir(), os.TempDir()), "worktree is outside the repo tree")
	assert.FileExists(t, filepath.Join(ws.Dir(), "README.md"), "the worktree is a checkout of the repo")

	// §3.1: the broadened ctxloom-config excludes are written to the common-dir
	// info/exclude (NOT the tracked .gitignore).
	excl, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	require.NoError(t, err, "the common-dir info/exclude must exist")
	for _, pat := range []string{".mcp.json", ".claude/", ".kiro/", ".ctxloom/cache/"} {
		assert.Contains(t, string(excl), pat, "exclude covers %q", pat)
	}
	assert.NoFileExists(t, filepath.Join(repo, ".gitignore"), "excludes must NOT touch the tracked .gitignore")

	// wtDir (captured above, before teardown clears Cleanup) is reused here.

	// Run a "no-op" in the worktree — it stays clean, so the WIP-safe teardown
	// removes it.
	require.NoError(t, ws.Cleanup(), "WIP-safe teardown removes a clean worktree")
	assert.NoDirExists(t, wtDir, "the worktree dir is gone after cleanup")

	// `git worktree list` has no leftover pointing at our path.
	out := gitOut(t, repo, "worktree", "list", "--porcelain")
	assert.NotContains(t, out, wtDir, "no leftover worktree after WIP-safe teardown")
}

// TestWorktreePolicy_RealGitPreservesInnerWIP proves the nested-worktree WIP guard
// against real git: a nested inner worktree with an uncommitted file aborts the
// teardown, so BOTH the inner and outer survive (the exact footgun git's own
// dirty-check misses).
func TestWorktreePolicy_RealGitPreservesInnerWIP(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping the nested-WIP integration test")
	}
	ctx := context.Background()
	repo := initRealRepo(t)

	pol := NewWorktree(git.NewExec(), "")
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-nest")
	require.NoError(t, err)
	outer := ws.Dir()
	// Safety net registered IMMEDIATELY: the test's own manual cleanup below
	// (gitRun ... worktree remove --force) is plain sequential code, not a
	// defer/t.Cleanup — this test's whole point is proving the outer/inner
	// dirs survive an intentional WIP guard, so any earlier require/assert
	// failure or panic skips that manual cleanup entirely and leaks both
	// checkouts under the OS temp dir. A raw RemoveAll here is safe even
	// though these are real `git worktree add` targets: the repo (and its
	// .git metadata) is a throwaway t.TempDir() that vanishes right after,
	// so leftover `git worktree list` bookkeeping doesn't matter — only the
	// disk space does. LIFO cleanup order removes inner before outer.
	t.Cleanup(func() { _ = os.RemoveAll(outer) })
	cleanupConfigHome(t, ws)

	// Simulate claude's EnterWorktree: a nested worktree INSIDE ours, with WIP.
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")
	require.NoError(t, os.MkdirAll(filepath.Dir(inner), 0o755))
	gitRun(t, repo, "worktree", "add", "--detach", inner, "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(inner, "wip.txt"), []byte("uncommitted"), 0o644))
	t.Cleanup(func() { _ = os.RemoveAll(inner) })

	require.NoError(t, ws.Cleanup(), "cleanup never errors")

	// Both survive — WIP is sacred.
	assert.DirExists(t, inner, "the nested inner worktree with WIP is preserved")
	assert.FileExists(t, filepath.Join(inner, "wip.txt"), "the inner's uncommitted work is intact")
	assert.DirExists(t, outer, "the outer is left in place because removing it would destroy the inner")

	// Manual cleanup so the test leaves no worktrees behind on the normal path
	// (keeps git's own worktree bookkeeping tidy); the t.Cleanup registrations
	// above are the leak-safety net for the abnormal paths.
	gitRun(t, repo, "worktree", "remove", "--force", inner)
	gitRun(t, repo, "worktree", "remove", "--force", outer)
}

// cleanupConfigHome registers a raw, unmutated os.RemoveAll safety net for the
// workspace's per-agent config-home dir (provisionConfigHome's ctxloom-cfg-*
// scratch), reaching the unexported field directly since this file shares
// worktree.go's package. ws.Cleanup() already removes configHome itself
// unconditionally (unlike the WIP-gated worktree checkout), but that removal
// runs through the SAME production code gremlins mutates — a mutant that
// breaks it is "killed" by an assertion failing elsewhere while, as a genuine
// side effect, also leaving this dir on disk. Registering a redundant
// test-side removal (plain stdlib, never a mutation target) is the only way
// to keep that expected mutant-kill from also being a real leak.
func cleanupConfigHome(t *testing.T, ws Workspace) {
	t.Helper()
	concrete, ok := ws.(*worktreeWorkspace)
	if !ok || concrete.configHome == "" {
		return
	}
	home := concrete.configHome
	t.Cleanup(func() { _ = os.RemoveAll(home) })
}

// requireCleanWorkspace registers unconditional, mutation-proof safety-net
// removal for every REAL on-disk scratch resource a prepared workspace may
// hold, for tests across this package that call PrepareWorkspace directly
// (not just the worktree-integration ones above). Call it IMMEDIATELY after a
// successful PrepareWorkspace, before any other test code that could
// fail/panic and skip a later manual/mid-test cleanup.
//
//   - *worktreeWorkspace: the checkout (ws.Dir()) plus its config-home — see
//     cleanupConfigHome. The checkout is WIP-gated in ws.Cleanup() by design
//     (production code, correctly conservative for a real developer); tests
//     that deliberately dirty their own throwaway checkout must not rely on
//     that guard clearing it.
//   - *containerWorkspace: ONLY scratchRoot (the host socket/config-overlay
//     scratch from containerScratchBase(), always OS-temp on Linux regardless
//     of session/harp scoping). Dir() is intentionally EXCLUDED here: for a
//     container workspace it is the *identical-path* live project directory
//     (or a composed worktree checkout, itself already handled by the
//     worktreeWorkspace case via baseCleanup's own type), never a dedicated
//     scratch path — force-removing it would risk deleting the very repo the
//     test is using.
func requireCleanWorkspace(t *testing.T, ws Workspace) {
	t.Helper()
	if ws == nil {
		return
	}
	switch concrete := ws.(type) {
	case *worktreeWorkspace:
		if dir := concrete.dir; dir != "" {
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
		}
		cleanupConfigHome(t, ws)
	case *containerWorkspace:
		if root := concrete.scratchRoot; root != "" {
			t.Cleanup(func() { _ = os.RemoveAll(root) })
		}
	}
}

// initRealRepo creates a temp git repo with one commit and returns its path.
func initRealRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed"), 0o644))
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "seed")
	return dir
}

// gitRun runs a git command in dir with a stable identity, failing the test on
// error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitCmd(dir, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// gitOut runs a git command in dir and returns its combined output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmd(dir, args...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
		"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com",
		// Isolate from the developer's / CI's GLOBAL and SYSTEM git config: a
		// global commit.gpgsign, core.hooksPath, or init.templateDir would
		// otherwise leak in and corrupt these real-git init/commit/worktree
		// operations (this file is UN-tagged, so it also runs under `just test`).
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	return cmd
}
