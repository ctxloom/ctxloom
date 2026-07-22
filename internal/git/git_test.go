package git

import (
	"context"
	"os"
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

// TestExecGit_CommonDir_SymlinkedWorktreePath covers live-gag's edge case
// (container-runtime-bugs.plan.md §2.2.3): a worktree reached through a
// SYMLINKED alias path must still resolve to the common dir's REAL absolute
// path, not a path that runs through the symlink — the isolation container
// axis mounts CommonDir's return value identical-path (gitCommonDirMount), so
// a symlink-relative answer would mount the WRONG (or a nonexistent-outside-
// the-mount) path inside the container. `git rev-parse --git-common-dir`
// itself resolves to the physical path here (this is git's own behavior, not
// ctxloom's CommonDir); this test pins that CommonDir does not accidentally
// re-introduce the symlink alias when it absolutizes a relative answer.
func TestExecGit_CommonDir_SymlinkedWorktreePath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping exec git integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	wt := filepath.Join(t.TempDir(), "wt")
	require.NoError(t, g.WorktreeAdd(ctx, repo, wt, "HEAD"))

	// A symlinked ALIAS to the worktree — the scenario a repo reached via a
	// symlinked path (a symlinked project dir, or a worktree on a symlinked
	// mount) exercises.
	link := filepath.Join(t.TempDir(), "wt-link")
	require.NoError(t, os.Symlink(wt, link))

	viaReal, err := g.CommonDir(ctx, wt)
	require.NoError(t, err)
	viaLink, err := g.CommonDir(ctx, link)
	require.NoError(t, err)

	// Both must resolve to the SAME real, absolute common dir — the one an
	// identical-path container mount actually needs — regardless of which
	// alias path git was invoked from.
	assert.Equal(t, resolvePath(t, viaReal), resolvePath(t, viaLink),
		"CommonDir must resolve to the same real path whether reached directly or through a symlinked alias")
	assert.True(t, filepath.IsAbs(viaLink), "CommonDir must return an absolute path even via a symlinked cwd")
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

// TestExecGit_CurrentBranch pins both shapes: an attached checkout reports
// its real branch name, and a detached one (exactly what `git worktree add
// --detach` leaves every ctxloom-created worktree in) reports git's own
// sentinel "HEAD" — the dirty-tree commit handler's guard against
// auto-committing inside a ref-less checkout depends on telling these apart.
func TestExecGit_CurrentBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping CurrentBranch integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t) // initRepo names the branch "main"

	branch, err := g.CurrentBranch(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, "main", branch)

	wt := filepath.Join(t.TempDir(), "detached-wt")
	require.NoError(t, g.WorktreeAdd(ctx, repo, wt, "HEAD"))
	detached, err := g.CurrentBranch(ctx, wt)
	require.NoError(t, err)
	assert.Equal(t, "HEAD", detached, `git's own sentinel for detached HEAD`)
}

// TestExecGit_CommitAll_StagesAndVerifies proves the real commit lands
// (staged tracked mod + untracked file both captured), advances HEAD, and
// its self-reported changedFiles list is the actual post-commit diff —
// exercising the mechanism the dirty-tree commit handler leans on instead of
// trusting a bare "commit succeeded" (this codebase's documented empty-commit
// pre-commit-hook bug is exactly what that verification exists to catch).
func TestExecGit_CommitAll_StagesAndVerifies(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping CommitAll integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	require.NoError(t, writeFile(filepath.Join(repo, "README.md"), "changed"))            // tracked mod
	require.NoError(t, writeFile(filepath.Join(repo, "new/untracked.go"), "package new")) // untracked

	preOut, err := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	preSHA := strings.TrimSpace(string(preOut))

	sha, changed, err := g.CommitAll(ctx, repo, "test commit\n\nbody line")
	require.NoError(t, err)
	assert.NotEqual(t, preSHA, sha, "HEAD advanced")
	assert.ElementsMatch(t, []string{"README.md", "new/untracked.go"}, changed,
		"both the tracked mod AND the previously-untracked file were staged and committed")

	dirty, err := g.IsDirty(ctx, repo)
	require.NoError(t, err)
	assert.False(t, dirty, "nothing left uncommitted")
}

// TestExecGit_DiffPatch_ApplyPatch_RoundTrip proves the copy handler's
// tracked-changes mechanism end to end: a patch captured from one checkout
// (modification + deletion) applies cleanly to a SEPARATE checkout at the
// same HEAD, reproducing the same uncommitted working-tree state there.
func TestExecGit_DiffPatch_ApplyPatch_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping DiffPatch/ApplyPatch integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)
	commit(t, repo, "keep.txt", "keep me", "add keep.txt")
	commit(t, repo, "gone.txt", "delete me", "add gone.txt")

	// Uncommitted in the SOURCE: modify one tracked file, delete another.
	require.NoError(t, writeFile(filepath.Join(repo, "keep.txt"), "keep me, modified"))
	require.NoError(t, rm(filepath.Join(repo, "gone.txt")))

	patch, err := g.DiffPatch(ctx, repo)
	require.NoError(t, err)
	require.NotEmpty(t, patch)

	// A second, independent worktree checked out at the SAME HEAD, clean.
	target := filepath.Join(t.TempDir(), "copy-target")
	require.NoError(t, g.WorktreeAdd(ctx, repo, target, "HEAD"))
	dirtyBefore, err := g.IsDirty(ctx, target)
	require.NoError(t, err)
	require.False(t, dirtyBefore)

	require.NoError(t, g.ApplyPatch(ctx, target, patch))

	got, err := os.ReadFile(filepath.Join(target, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "keep me, modified", string(got), "the modification reproduced")
	_, err = os.Stat(filepath.Join(target, "gone.txt"))
	assert.True(t, os.IsNotExist(err), "the deletion reproduced")

	dirtyAfter, err := g.IsDirty(ctx, target)
	require.NoError(t, err)
	assert.True(t, dirtyAfter, "the reproduced changes are UNCOMMITTED WIP in the target, exactly like the source")
}

// TestExecGit_ApplyPatch_EmptyIsNoop proves an empty patch (nothing tracked
// to reproduce — the copy handler's "untracked files only" case) never
// invokes git in a way that could fail or mutate anything.
func TestExecGit_ApplyPatch_EmptyIsNoop(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping ApplyPatch integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)
	assert.NoError(t, g.ApplyPatch(ctx, repo, ""))
	assert.NoError(t, g.ApplyPatch(ctx, repo, "   \n"))
}

// TestExecGit_ListUntracked proves the untracked inventory honors BOTH
// .gitignore and .git/info/exclude (the same noise the copy handler must
// never try to reproduce, since it never counted as dirty in the first
// place), and reports a genuinely untracked file.
func TestExecGit_ListUntracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping ListUntracked integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	require.NoError(t, writeFile(filepath.Join(repo, "fresh.go"), "package fresh"))
	require.NoError(t, writeFile(filepath.Join(repo, "ignored-by-gitignore.log"), "noise"))
	require.NoError(t, writeFile(filepath.Join(repo, ".gitignore"), "*.log\n"))
	excludeCmd := exec.CommandContext(ctx, "git", "-C", repo, "rev-parse", "--git-path", "info/exclude")
	out, err := excludeCmd.Output()
	require.NoError(t, err)
	excludePath := filepath.Join(repo, strings.TrimSpace(string(out)))
	require.NoError(t, writeFile(excludePath, "ignored-by-info-exclude.tmp\n"))
	require.NoError(t, writeFile(filepath.Join(repo, "ignored-by-info-exclude.tmp"), "noise"))

	files, err := g.ListUntracked(ctx, repo)
	require.NoError(t, err)
	assert.Contains(t, files, "fresh.go")
	assert.NotContains(t, files, "ignored-by-gitignore.log")
	assert.NotContains(t, files, "ignored-by-info-exclude.tmp")
	assert.Contains(t, files, ".gitignore", ".gitignore itself is untracked and not itself excluded by anything")
}
