package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestFindAppDir_WorktreeSignpost is the acceptance gate for task brown-canal
// (2026-07-09 revision): a linked git worktree with no .ctxloom of its own is
// a fail-loud signpost finding, not a silent resolution as a foreign/empty
// project. findAppDir records the finding through strictness and continues on
// today's fallback — the startup choke owners (`ctxloom run`/`mcp`/`acp`)
// abort on it pre-launch (exit 3) unless --degraded. See worktreeSignpost for
// the implementation and its load-bearing precedence comment.
func TestFindAppDir_WorktreeSignpost(t *testing.T) {
	t.Run("linked_worktree_without_own_ctxloom_records_a_finding", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		main, linked := realGitWorktreeFixture(t)
		testsupport.ChangeDir(t, linked)

		mark := strictness.Checkpoint()
		findAppDir(afero.NewOsFs()) // resolution proceeds; the gate owner aborts, not findAppDir

		findings := strictness.Since(mark)
		require.Len(t, findings, 1,
			"a linked worktree with no .ctxloom must record exactly one fatal finding, not silently resolve as a foreign project")
		f := findings[0]
		assert.Equal(t, strictness.ClassConfig, f.Class)
		assert.Contains(t, f.Message, "linked git worktree", "the finding names what happened")
		assert.Contains(t, f.Message, main, "the finding names the resolved main root")
		assert.Contains(t, f.FixIt, "run ctxloom from "+main, "fix-it path 1: run from the main worktree")
		assert.Contains(t, f.FixIt, "ctxloom init", "fix-it path 2: init here to opt out as a deliberately separate project")
	})

	t.Run("finding_is_deduped_across_repeat_loads", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		_, linked := realGitWorktreeFixture(t)
		testsupport.ChangeDir(t, linked)

		mark := strictness.Checkpoint()
		findAppDir(afero.NewOsFs())
		findAppDir(afero.NewOsFs()) // config.Load runs several times per process

		assert.Len(t, strictness.Since(mark), 1,
			"FailOnce must collapse the same worktree finding within one startup window")
	})

	t.Run("linked_worktree_with_own_ctxloom_is_unaffected", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		_, linked := realGitWorktreeFixture(t)
		require.NoError(t, os.MkdirAll(filepath.Join(linked, AppDirName), 0o755))
		testsupport.ChangeDir(t, linked)

		mark := strictness.Checkpoint()
		path, src := findAppDir(afero.NewOsFs())

		assert.Empty(t, strictness.Since(mark),
			"an opted-out worktree (own .ctxloom) must behave exactly as today — no finding")
		assert.Equal(t, filepath.Join(linked, AppDirName), path)
		assert.Equal(t, SourceProject, src)
	})

	t.Run("plain_directory_is_unaffected", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		testsupport.ChangeDir(t, t.TempDir())

		mark := strictness.Checkpoint()
		findAppDir(afero.NewOsFs())
		assert.Empty(t, strictness.Since(mark),
			"a plain, non-worktree directory must never record a worktree finding")
	})

	t.Run("main_worktree_is_unaffected", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		main, _ := realGitWorktreeFixture(t)
		testsupport.ChangeDir(t, main)

		mark := strictness.Checkpoint()
		findAppDir(afero.NewOsFs())
		assert.Empty(t, strictness.Since(mark),
			"the main worktree itself must never record a worktree finding")
	})

	t.Run("stale_pointer_names_the_missing_main_root", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		main, linked := realGitWorktreeFixture(t)
		require.NoError(t, os.RemoveAll(main))
		testsupport.ChangeDir(t, linked)

		mark := strictness.Checkpoint()
		findAppDir(afero.NewOsFs())

		findings := strictness.Since(mark)
		require.Len(t, findings, 1)
		assert.Contains(t, findings[0].Message, "missing or unreadable",
			"a stale gitdir pointer is its own diagnosis, not the generic signpost")
		assert.Contains(t, findings[0].FixIt, "git worktree prune")
	})

	t.Run("degraded_mode_records_nothing_and_falls_back", func(t *testing.T) {
		testsupport.Isolate(t)
		resetStrictness(t)
		strictness.SetDegraded(true)
		_, linked := realGitWorktreeFixture(t)

		// Control: resolve from a plain (non-worktree) directory nested at the
		// same depth as linked, so both walks cross an identical ancestor
		// chain above them. Degraded mode's result from the linked worktree
		// must match this exactly — "falls back to today's behavior" made
		// precise and independent of whatever .ctxloom directories happen to
		// pre-exist above the test's own temp tree.
		plainSibling := filepath.Join(t.TempDir(), "plain-dir")
		require.NoError(t, os.Mkdir(plainSibling, 0o755))
		testsupport.ChangeDir(t, plainSibling)
		wantPath, wantSrc := findAppDir(afero.NewOsFs())

		testsupport.ChangeDir(t, linked)
		mark := strictness.Checkpoint()
		path, src := findAppDir(afero.NewOsFs())

		assert.Empty(t, strictness.Since(mark),
			"degraded mode must not collect the finding (warn-and-continue only)")
		assert.Equal(t, wantSrc, src)
		assert.Equal(t, wantPath, path)
	})
}

// resetStrictness restores pristine strict-mode state for a test and registers
// cleanup, so the package-global finding collector never bleeds between tests
// (mirrors internal/operations' helper of the same name).
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// realGitWorktreeFixture creates a real git repo (main, with one commit) and a
// LINKED worktree of it (linked) via `git worktree add`, each under its own
// fresh t.TempDir(). Skips the test if git isn't on PATH. Returns both roots
// as absolute, symlink-resolved paths so they compare equal to whatever git
// itself resolved into the gitdir pointer (mirrors
// internal/projectroot's identically-named test helper; kept local here since
// Go does not let a _test.go file export helpers across packages).
func realGitWorktreeFixture(t *testing.T) (main, linked string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	mainDir := t.TempDir()
	runGit(t, mainDir, "init", "-q")
	runGit(t, mainDir, "config", "user.email", "test@example.com")
	runGit(t, mainDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(mainDir, "f.txt"), []byte("x"), 0o644))
	runGit(t, mainDir, "add", "f.txt")
	runGit(t, mainDir, "commit", "-q", "-m", "init")

	linkedDir := filepath.Join(t.TempDir(), "linked-wt")
	runGit(t, mainDir, "worktree", "add", "-q", "-b", "wt-branch", linkedDir)

	main, err := filepath.EvalSymlinks(mainDir)
	require.NoError(t, err)
	linked, err = filepath.EvalSymlinks(linkedDir)
	require.NoError(t, err)
	return main, linked
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}
