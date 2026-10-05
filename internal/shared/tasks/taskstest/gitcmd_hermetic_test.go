package taskstest

import (
	"os"
	"path/filepath"
	"strings"
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

// CLAIM: a test's git cannot be reconfigured by config overrides inherited from
// whatever ran the suite. `git -c` exports them to every child — as
// GIT_CONFIG_PARAMETERS, and as GIT_CONFIG_COUNT with its GIT_CONFIG_KEY_<n> /
// GIT_CONFIG_VALUE_<n> pairs — so a suite run from a hook or an alias would
// hand every fixture's git a configuration that outranks the fixture's own.
func TestGitCmd_InheritedConfigOverridesDoNotReachTheTestsGit(t *testing.T) {
	const fixtureName, injectedName = "Fixture Identity", "Injected Override"
	for name, env := range map[string][][2]string{
		"count-key-value": {
			{"GIT_CONFIG_COUNT", "1"},
			{"GIT_CONFIG_KEY_0", "user.name"},
			{"GIT_CONFIG_VALUE_0", injectedName},
		},
		"parameters": {
			{"GIT_CONFIG_PARAMETERS", "'user.name'='" + injectedName + "'"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			Git(t, dir, nil, "init", "-q")
			Git(t, dir, nil, "config", "user.name", fixtureName)
			for _, kv := range env {
				t.Setenv(kv[0], kv[1])
			}

			out, err := GitCmd(dir, nil, "config", "--get", "user.name").CombinedOutput()
			require.NoError(t, err, "%s", out)
			assert.Equal(t, fixtureName, strings.TrimSpace(string(out)),
				"the fixture's git read its identity from an inherited config override")
		})
	}
}
