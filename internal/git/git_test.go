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

// TestParseLogEntries pins two behaviours across every shape the
// --pretty=format output can take, so neither can be changed silently.
//
// The row's stated consequence — that a zero Date "flows into downstream
// since-filtering" — does not hold: the since-window is applied by git itself
// (`git log --since=…`, see LogSince), and no consumer filters on
// LogEntry.Date. It is rendered (task_triggers_query, triggers/prompt) and
// hashed into the verdict-cache fingerprint, and nothing else. What matters
// instead is that an unparseable date must stay DETECTABLE rather than pass
// for a real timestamp: time.Time{}.IsZero() is that signal, and a consumer
// that wants to distinguish the two has it.
func TestParseLogEntries(t *testing.T) {
	sep := logFieldSep

	t.Run("parses the ordinary three-field shape", func(t *testing.T) {
		got := parseLogEntries("abc123" + sep + "2026-07-30T12:00:00+00:00" + sep + "add a thing")
		require.Len(t, got, 1)
		assert.Equal(t, "abc123", got[0].SHA)
		assert.Equal(t, "add a thing", got[0].Subject)
		assert.False(t, got[0].Date.IsZero())
		assert.Equal(t, 2026, got[0].Date.UTC().Year())
	})

	t.Run("an empty subject is preserved, not treated as malformed", func(t *testing.T) {
		got := parseLogEntries("abc123" + sep + "2026-07-30T12:00:00+00:00" + sep)
		require.Len(t, got, 1, "git emits this for a commit with an empty subject line")
		assert.Empty(t, got[0].Subject)
	})

	t.Run("a separator inside the subject stays in the subject", func(t *testing.T) {
		got := parseLogEntries("abc123" + sep + "2026-07-30T12:00:00+00:00" + sep + "sub" + sep + "ject")
		require.Len(t, got, 1, "SplitN(…, 3) keeps the remainder together — the commit is not lost")
		assert.Equal(t, "sub"+sep+"ject", got[0].Subject)
	})

	t.Run("a line with too few fields is skipped, never half-parsed", func(t *testing.T) {
		got := parseLogEntries("garbage-with-no-separators\nabc123" + sep + "2026-07-30T12:00:00+00:00" + sep + "real")
		require.Len(t, got, 1, "the malformed line must not produce an entry with a SHA-shaped subject")
		assert.Equal(t, "abc123", got[0].SHA)
	})

	t.Run("an unparseable date keeps the commit and stays detectable", func(t *testing.T) {
		got := parseLogEntries("abc123" + sep + "not-a-date" + sep + "real commit")
		require.Len(t, got, 1, "a bad date must not discard the commit itself — the SHA and subject are still evidence")
		assert.True(t, got[0].Date.IsZero(),
			"an unknown date must remain distinguishable from a real one, never a plausible-looking substitute")
		assert.Equal(t, "real commit", got[0].Subject)
	})

	t.Run("blank lines and CR line endings", func(t *testing.T) {
		got := parseLogEntries("abc123" + sep + "2026-07-30T12:00:00+00:00" + sep + "one\r\n\n")
		require.Len(t, got, 1)
		assert.Equal(t, "one", got[0].Subject, "a CRLF terminator is not part of the subject")
	})
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

	require.NoError(t, g.WorktreeRemove(ctx, repo, wt))
	list, err = g.WorktreeList(ctx, repo)
	require.NoError(t, err)
	assert.False(t, containsPath(list, wt), "the worktree is gone after remove")
}

// TestExecGit_CommonDir_SymlinkedWorktreePath covers an edge case
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
	// Untracked, never committed.
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

// TestExecGit_RepoDirs_IncludesAncestors pins that the inventory answers
// existence-style triggers ("once package X exists"), and a directory whose
// content all lives in SUBdirectories is still a directory that exists: with
// only internal/signing/keys/pem.go in the repo, "internal/signing" and
// "internal" must both be inventoried. Recording only each file's IMMEDIATE
// parent under-reports exactly the question this evidence was written to
// answer, and the model reads the absence as proof the package does not exist.
func TestExecGit_RepoDirs_IncludesAncestors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping exec git integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	commit(t, repo, "internal/signing/keys/pem.go", "package keys", "add nested tracked pkg")
	// Untracked and equally nested — the same claim must hold for work that
	// lives in no commit.
	require.NoError(t, writeFile(filepath.Join(repo, "internal/fresh/deep/nested/b.go"), "package nested"))

	dirs, err := g.RepoDirs(ctx, repo, 0)
	require.NoError(t, err)

	for _, want := range []string{
		"internal", "internal/signing", "internal/signing/keys",
		"internal/fresh", "internal/fresh/deep", "internal/fresh/deep/nested",
	} {
		assert.Contains(t, dirs, want, "every ancestor directory exists and must be inventoried")
	}
	assert.NotContains(t, dirs, ".", "the repo root is not a directory entry")
	assert.NotContains(t, dirs, "", "no empty entry")
	assert.IsNonDecreasing(t, dirs, "the inventory stays sorted")
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

// TestAttachCommitFiles_UnresolvableCommit pins that when the per-commit
// `git diff-tree` call fails, the entry used to keep a nil Files list, which
// is byte-for-byte what a commit that genuinely touched no files looks like.
// The consumer that filters commits by path (internal/operations'
// queryGitLogPath) then reads a failed lookup as "this commit did not touch
// the path" and answers a trigger with a confident negative built on evidence
// it never gathered. The miss must be recorded, and must still not be fatal.
func TestAttachCommitFiles_UnresolvableCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping diff-tree integration test")
	}
	repo := initRepo(t)
	sha := commit(t, repo, "a.txt", "a", "add a")

	entries := []LogEntry{
		{SHA: "0000000000000000000000000000000000000000", Subject: "unreachable"},
		{SHA: sha, Subject: "add a"},
	}
	attachCommitFiles(context.Background(), repo, entries)

	assert.Nil(t, entries[0].Files, "a failed diff-tree leaves no files")
	assert.True(t, entries[0].FilesUnknown,
		"a failed lookup must be distinguishable from a commit that touched nothing")
	assert.Equal(t, "unreachable", entries[0].Subject, "the commit summary survives — the miss is not fatal")

	assert.Equal(t, []string{"a.txt"}, entries[1].Files, "the resolvable commit is unaffected")
	assert.False(t, entries[1].FilesUnknown, "a successful lookup is never marked unknown")
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

// TestExecGit_MergedBranches pins the primitive doctor's foreign-worktree
// check needs to tell "merged" from "unmerged" for real rather than assuming
// one or the other: a branch identical to main's tip is merged, a branch
// carrying a commit main never got is not, and an empty ref resolves to
// repoDir's own current branch (mirroring CurrentBranch's default).
func TestExecGit_MergedBranches(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping MergedBranches integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t) // branch "main", one commit

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=ctxloom", "GIT_AUTHOR_EMAIL=ctxloom@example.com",
			"GIT_COMMITTER_NAME=ctxloom", "GIT_COMMITTER_EMAIL=ctxloom@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// merged-branch never diverges from main's tip: fully merged.
	run("branch", "merged-branch")

	// unmerged-branch carries a commit main does not have.
	run("checkout", "-b", "unmerged-branch")
	require.NoError(t, writeFile(filepath.Join(repo, "feature.txt"), "feature work"))
	run("add", "feature.txt")
	run("commit", "-m", "feature work")
	run("checkout", "main")

	merged, err := g.MergedBranches(ctx, repo, "main")
	require.NoError(t, err)
	assert.Contains(t, merged, "merged-branch")
	assert.NotContains(t, merged, "unmerged-branch",
		"a branch with a commit main never got must never be reported merged")

	defaulted, err := g.MergedBranches(ctx, repo, "")
	require.NoError(t, err)
	assert.ElementsMatch(t, merged, defaulted,
		"an empty ref must resolve to repoDir's own current branch, same as explicitly naming it")
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

// TestExecGit_CommitAll_UnbornHEAD pins that in a repository with zero
// commits HEAD is UNBORN: `git rev-parse HEAD` fails, and CommitAll resolved
// the pre-commit HEAD unconditionally, so it refused outright. That is the
// one moment a project has the most unpreserved work and the least history to
// lose — CommitAll must create the ROOT commit and still report the files it
// captured, since its caller refuses any commit reporting no content.
func TestExecGit_CommitAll_UnbornHEAD(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping CommitAll integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepoUnborn(t)

	require.NoError(t, writeFile(filepath.Join(repo, "README.md"), "first"))
	require.NoError(t, writeFile(filepath.Join(repo, "pkg/a.go"), "package pkg"))

	sha, changed, err := g.CommitAll(ctx, repo, "initial commit")
	require.NoError(t, err, "a zero-commit repo must still be committable")
	assert.NotEmpty(t, sha, "the root commit's SHA is reported")
	assert.ElementsMatch(t, []string{"README.md", "pkg/a.go"}, changed,
		"a root commit's changed-file list is every file in it, not the empty list its caller treats as a failed commit")

	dirty, err := g.IsDirty(ctx, repo)
	require.NoError(t, err)
	assert.False(t, dirty, "nothing left uncommitted")
}

// TestExecGit_CommitAll_NonRepoStillFails pins the other half: the unborn-HEAD
// accommodation must not turn a genuinely broken target (a bare directory that
// is no repository at all) into a silent success.
func TestExecGit_CommitAll_NonRepoStillFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping CommitAll integration test")
	}
	_, _, err := NewExec().CommitAll(context.Background(), t.TempDir(), "nope")
	require.Error(t, err, "a directory that is not a repository must fail loudly")
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

	applied, err := g.ApplyPatch(ctx, target, patch)
	require.NoError(t, err)
	require.True(t, applied)

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
	applied, err := g.ApplyPatch(ctx, repo, "")
	assert.NoError(t, err)
	assert.False(t, applied)
	applied, err = g.ApplyPatch(ctx, repo, "   \n")
	assert.NoError(t, err)
	assert.False(t, applied)
}

// TestExecGit_ApplyPatch_ReportsWhetherItActuallyApplied pins that an
// empty patch and a real patch must be distinguishable at the type level, not
// just "no error" for both. A caller that skips ApplyPatch on an empty patch
// (as applyCopySnapshot does today) still needs a way to notice if THIS
// method silently no-ops for a reason other than "genuinely nothing to
// apply" — e.g. a future call site that stops guarding the empty case.
func TestExecGit_ApplyPatch_ReportsWhetherItActuallyApplied(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping ApplyPatch integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	applied, err := g.ApplyPatch(ctx, repo, "")
	require.NoError(t, err)
	assert.False(t, applied, "an empty patch must report applied=false, not just nil error")

	applied, err = g.ApplyPatch(ctx, repo, "   \n")
	require.NoError(t, err)
	assert.False(t, applied, "a whitespace-only patch must report applied=false")

	require.NoError(t, writeFile(filepath.Join(repo, "keep.txt"), "keep me"))
	commit(t, repo, "keep.txt", "keep me", "add keep.txt")
	require.NoError(t, writeFile(filepath.Join(repo, "keep.txt"), "keep me, modified"))
	patch, err := g.DiffPatch(ctx, repo)
	require.NoError(t, err)
	require.NotEmpty(t, patch)
	require.NoError(t, run(ctx, repo, "checkout", "--", "keep.txt"))

	applied, err = g.ApplyPatch(ctx, repo, patch)
	require.NoError(t, err)
	assert.True(t, applied, "a real patch must report applied=true")
}

// TestExecGit_HasIgnoredContent pins that IsDirty deliberately
// treats gitignored/excluded content as clean (that is what lets a prepared
// agent worktree's OWN delivered noise — .claude/, CLAUDE.md, .ctxloom/cache/
// — coexist with a real WIP check). But teardown needs a SEPARATE signal for
// "this worktree holds ignored files that would be destroyed", which nothing
// in this package exposes today.
func TestExecGit_HasIgnoredContent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping HasIgnoredContent integration test")
	}
	ctx := context.Background()
	g := NewExec()
	repo := initRepo(t)

	has, err := g.HasIgnoredContent(ctx, repo)
	require.NoError(t, err)
	assert.False(t, has, "a fresh repo with no ignored content reports false")

	// The .gitignore itself must be COMMITTED (clean, tracked) so the only
	// remaining signal is the ignored file it excludes — otherwise the
	// untracked .gitignore alone would make IsDirty true too, muddying what
	// this test is isolating.
	commit(t, repo, ".gitignore", "secret.txt\n", "add .gitignore")
	require.NoError(t, writeFile(filepath.Join(repo, "secret.txt"), "do not lose me"))

	has, err = g.HasIgnoredContent(ctx, repo)
	require.NoError(t, err)
	assert.True(t, has, "an ignored file the WIP check would miss must be detected")

	dirty, err := g.IsDirty(ctx, repo)
	require.NoError(t, err)
	assert.False(t, dirty, "IsDirty's existing contract is unchanged: ignored content alone is still not \"dirty\"")
}

// TestExecGit_DoesNotInheritGitRepoEnvVars pins that GIT_DIR/GIT_WORK_TREE/
// GIT_INDEX_FILE/GIT_COMMON_DIR override cmd.Dir completely when inherited from
// the calling process's environment. A ctxloom process that itself runs inside
// (or is launched by something that sets) one of these must not have every git
// operation in this package silently redirected to a DIFFERENT repository.
func TestExecGit_DoesNotInheritGitRepoEnvVars(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping env-leak integration test")
	}
	ctx := context.Background()
	g := NewExec()

	repoA := initRepo(t)
	repoB := initRepo(t)
	commit(t, repoB, "only-in-b.txt", "b", "add only-in-b.txt")

	// Point the poisoned env vars at repo B while operating on repo A.
	t.Setenv("GIT_DIR", filepath.Join(repoB, ".git"))
	t.Setenv("GIT_WORK_TREE", repoB)

	branch, err := g.CurrentBranch(ctx, repoA)
	require.NoError(t, err)
	assert.Equal(t, "main", branch)

	changes, err := g.WorkingChanges(ctx, repoA, 0)
	require.NoError(t, err)
	assert.Empty(t, changes, "repo A is clean; a leak would show repo B's uncommitted state instead")
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
