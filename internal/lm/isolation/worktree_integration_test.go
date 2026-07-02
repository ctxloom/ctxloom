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

	pol := NewWorktree(git.NewExec())
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-int")
	require.NoError(t, err, "PrepareWorkspace must add a worktree in a real repo")

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

	// Capture the path before teardown (Cleanup clears Dir()).
	wtDir := ws.Dir()

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

	pol := NewWorktree(git.NewExec())
	ws, err := pol.PrepareWorkspace(ctx, repo, "member-nest")
	require.NoError(t, err)
	outer := ws.Dir()

	// Simulate claude's EnterWorktree: a nested worktree INSIDE ours, with WIP.
	inner := filepath.Join(outer, ".claude", "worktrees", "inner")
	require.NoError(t, os.MkdirAll(filepath.Dir(inner), 0o755))
	gitRun(t, repo, "worktree", "add", "--detach", inner, "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(inner, "wip.txt"), []byte("uncommitted"), 0o644))

	require.NoError(t, ws.Cleanup(), "cleanup never errors")

	// Both survive — WIP is sacred.
	assert.DirExists(t, inner, "the nested inner worktree with WIP is preserved")
	assert.FileExists(t, filepath.Join(inner, "wip.txt"), "the inner's uncommitted work is intact")
	assert.DirExists(t, outer, "the outer is left in place because removing it would destroy the inner")

	// Manual cleanup so the test leaves no worktrees behind.
	gitRun(t, repo, "worktree", "remove", "--force", inner)
	gitRun(t, repo, "worktree", "remove", "--force", outer)
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
		"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com")
	return cmd
}
