package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseWorktreeList covers the porcelain parser across the shapes git emits:
// an attached main worktree, a detached linked worktree, and a bare entry.
func TestParseWorktreeList(t *testing.T) {
	out := "worktree /repo/main\nHEAD abc123\nbranch refs/heads/main\n\n" +
		"worktree /tmp/ctxloom-wt-x\nHEAD abc123\ndetached\n\n" +
		"worktree /repo/bare.git\nbare\n"

	got := parseWorktreeList(out)
	require.Len(t, got, 3)

	assert.Equal(t, "/repo/main", got[0].Path)
	assert.Equal(t, "abc123", got[0].Head)
	assert.Equal(t, "refs/heads/main", got[0].Branch)
	assert.False(t, got[0].Detached)

	assert.Equal(t, "/tmp/ctxloom-wt-x", got[1].Path)
	assert.True(t, got[1].Detached)
	assert.Empty(t, got[1].Branch)

	assert.Equal(t, "/repo/bare.git", got[2].Path)
	assert.True(t, got[2].Bare)
}

// TestExecGit_Lifecycle exercises the real git-binary impl end-to-end in a temp
// repo: add a detached worktree, list it, mark clean/dirty, common-dir resolution,
// and remove. Skips cleanly when git is unavailable so the normal suite stays
// green.
func TestExecGit_Lifecycle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping exec git integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	assert.True(t, g.IsRepo(repo), "the initialized dir is a repo")
	assert.False(t, g.IsRepo(t.TempDir()), "a bare temp dir is not a repo")

	top, err := g.Toplevel(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, resolvePath(t, repo), resolvePath(t, top))

	common, err := g.CommonDir(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, resolvePath(t, filepath.Join(repo, ".git")), resolvePath(t, common))

	wt := filepath.Join(t.TempDir(), "wt")
	require.NoError(t, g.WorktreeAdd(ctx, repo, wt, "HEAD"))

	list, err := g.WorktreeList(ctx, repo)
	require.NoError(t, err)
	assert.True(t, containsPath(list, wt), "the new worktree appears in the repo-global list")

	dirty, err := g.IsDirty(ctx, wt)
	require.NoError(t, err)
	assert.False(t, dirty, "a fresh worktree is clean")

	require.NoError(t, writeFile(filepath.Join(wt, "new.txt"), "hi"))
	dirty, err = g.IsDirty(ctx, wt)
	require.NoError(t, err)
	assert.True(t, dirty, "an untracked file makes the worktree dirty (WIP)")
	require.NoError(t, rm(filepath.Join(wt, "new.txt")))

	require.NoError(t, g.WorktreeRemove(ctx, repo, wt, false))
	list, err = g.WorktreeList(ctx, repo)
	require.NoError(t, err)
	assert.False(t, containsPath(list, wt), "the worktree is gone after remove")
}

// TestExecGit_ListTracked proves the ls-files pathspec seam the worktree policy
// uses to find TRACKED per-agent config: a committed .mcp.json is returned, an
// untracked one is not, and an empty pathspec list matches nothing (never "every
// tracked file"). Skips cleanly when git is unavailable.
func TestExecGit_ListTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping ListTracked integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	// Track a .mcp.json; leave an untracked .claude/settings.json on disk.
	require.NoError(t, writeFile(filepath.Join(repo, ".mcp.json"), "{}"))
	require.NoError(t, writeFile(filepath.Join(repo, ".claude", "settings.json"), "{}"))
	addCmd := exec.CommandContext(ctx, "git", "add", ".mcp.json")
	addCmd.Dir = repo
	require.NoError(t, addCmd.Run())

	tracked, err := g.ListTracked(ctx, repo, ".mcp.json", ".claude/")
	require.NoError(t, err)
	assert.Equal(t, []string{".mcp.json"}, tracked, "only the tracked config is returned")

	empty, err := g.ListTracked(ctx, repo)
	require.NoError(t, err)
	assert.Empty(t, empty, "an empty pathspec list matches nothing")
}
