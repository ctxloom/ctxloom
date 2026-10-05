package taskstest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readConfig returns the enclosing repository's shared config, the file a
// leaked git invocation writes into: a linked worktree's gitdir keeps no config
// of its own, so `git init` and `git config` aimed at it land in the MAIN
// checkout's .git/config, which every worktree of that repository reads.
func readConfig(t *testing.T, main string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(main, ".git", "config"))
	require.NoError(t, err)
	return string(b)
}

// CLAIM: a test's git cannot be retargeted at an enclosing repository by an
// inherited GIT_DIR.
//
// `git bisect run` and every git hook export GIT_DIR to their children, and
// inside a linked worktree that value is <main>/.git/worktrees/<name>. A test
// suite run under either one — bisecting with an integration test is the case
// that happened — hands it to every git the fixtures spawn. `git init` then
// re-initialises THAT gitdir instead of the fixture's directory, guessing it
// bare because its basename is not ".git", and the fixture's identity writes
// follow it: core.bare=true and the test identity land in the real checkout's
// config, and the main checkout stops being a work tree.
func TestGit_InheritedGitDirCannotReachAnEnclosingRepo(t *testing.T) {
	main, linked := RealGitWorktreeFixture(t)
	gitDir := Git(t, linked, nil, "rev-parse", "--absolute-git-dir")
	before := readConfig(t, main)

	t.Setenv("GIT_DIR", gitDir)
	fresh := t.TempDir()
	Git(t, fresh, nil, "init", "-q")
	Git(t, fresh, nil, "config", "user.email", "test@example.com")
	Git(t, fresh, nil, "config", "user.name", "Test User")

	assert.Equal(t, before, readConfig(t, main),
		"a fixture's git wrote into the repository named by the inherited GIT_DIR")
	info, err := os.Stat(filepath.Join(fresh, ".git"))
	require.NoError(t, err, "git init did not initialise the directory it was given")
	assert.True(t, info.IsDir(), "the fixture's own repository was not created where it was asked for")
}

// CLAIM: a test's git cannot walk up out of the temp root into a repository
// that encloses it.
//
// A git command run in a directory that is not (yet) a repository searches
// every parent for one. When the temp root itself sits inside a checkout — a
// TMPDIR pointed into a worktree — that search finds the checkout, and a
// fixture's `git config` rewrites the developer's repository.
func TestGitCmd_DiscoveryStopsAtTheTempRoot(t *testing.T) {
	main, _ := RealGitWorktreeFixture(t)
	before := readConfig(t, main)

	tmp := filepath.Join(main, "tmp")
	dir := filepath.Join(tmp, "not-a-repo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Setenv("TMPDIR", tmp)

	out, err := GitCmd(dir, nil, "config", "user.email", "test@example.com").CombinedOutput()
	assert.Error(t, err, "git found a repository above the temp root and wrote to it: %s", out)
	assert.Equal(t, before, readConfig(t, main),
		"a fixture's git walked up out of the temp root into the enclosing repository")
}

// CLAIM: a test's git never runs in an implicit directory. An empty dir runs
// git in the test binary's working directory — the package directory, inside
// the real checkout — and a relative one resolves against it.
func TestGitCmd_RefusesAnImplicitDirectory(t *testing.T) {
	for _, dir := range []string{"", "relative/dir"} {
		err := GitCmd(dir, nil, "status").Run()
		assert.ErrorIs(t, err, ErrGitDirNotAbsolute, "dir %q", dir)
	}
}
