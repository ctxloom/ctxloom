package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestExecGit_RepoDirsAndWorkingChanges exercises the "what exists NOW"
// evidence the trigger evaluator needs: the directory inventory must include
// directories whose content is only UNTRACKED (uncommitted work lives in no
// commit, so commit history structurally cannot reveal it), and the working
// changes must report it.
func TestExecGit_RepoDirsAndWorkingChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping exec git integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	commit(t, repo, "internal/committed/a.go", "package a", "add committed pkg")
	// Untracked, never committed — the used-lurk case.
	require.NoError(t, writeFile(filepath.Join(repo, "internal/fresh/pkg/b.go"), "package b"))

	dirs, err := g.RepoDirs(ctx, repo, 0)
	require.NoError(t, err)
	assert.Contains(t, dirs, "internal/committed", "tracked directories are inventoried")
	assert.Contains(t, dirs, "internal/fresh/pkg", "UNTRACKED directories exist too — history cannot show them")

	changes, err := g.WorkingChanges(ctx, repo, 0)
	require.NoError(t, err)
	joined := strings.Join(changes, "\n")
	assert.Contains(t, joined, "internal/fresh/pkg/b.go", "uncommitted work is reported")

	capped, err := g.RepoDirs(ctx, repo, 1)
	require.NoError(t, err)
	assert.Len(t, capped, 1, "the inventory is bounded")
}

// TestExecGit_LogSince exercises the real git-binary impl: a since-bound
// window excludes the seed commit, each returned entry carries its changed
// files, and maxEntries caps the result even when more commits qualify.
func TestExecGit_LogSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping exec git integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t) // one seed commit: README.md

	cutoff := time.Now().Add(time.Second) // strictly after the seed commit
	time.Sleep(1100 * time.Millisecond)   // git --since has 1s resolution

	sha1 := commit(t, repo, "a.txt", "a", "add a")
	sha2 := commit(t, repo, "b.txt", "b", "add b")

	entries, err := g.LogSince(ctx, repo, cutoff, 0)
	require.NoError(t, err)
	require.Len(t, entries, 2, "the seed commit predates the cutoff and must be excluded")

	// Newest first.
	assert.Equal(t, sha2, entries[0].SHA)
	assert.Equal(t, "add b", entries[0].Subject)
	assert.Equal(t, []string{"b.txt"}, entries[0].Files)
	assert.Equal(t, sha1, entries[1].SHA)
	assert.Equal(t, []string{"a.txt"}, entries[1].Files)

	capped, err := g.LogSince(ctx, repo, cutoff, 1)
	require.NoError(t, err)
	assert.Len(t, capped, 1, "maxEntries caps the result")

	// The zero time means no lower bound: everything (including the seed) is
	// in range.
	all, err := g.LogSince(ctx, repo, time.Time{}, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 3)
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
