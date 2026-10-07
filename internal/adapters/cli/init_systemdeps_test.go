package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeBinDir builds a directory containing only symlinks to the REAL
// binaries named, so checkSystemDeps' exec.LookPath probes see exactly (and
// only) those binaries on PATH — a hermetic, no-mocking way to drive both the
// present and absent branches of a PATH-based dependency check.
func fakeBinDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		real, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("test environment has no %s on PATH to symlink from", name)
		}
		require.NoError(t, os.Symlink(real, filepath.Join(dir, name)))
	}
	return dir
}

// isolateGitEnv makes warnIfGitIdentityMissing's real `git config --get`
// hermetic: HOME/XDG_CONFIG_HOME repointed at a fresh, empty temp dir with
// system config disabled (GIT_CONFIG_NOSYSTEM) so `user.name`/`user.email`
// never see the host's real config. Without this, checkSystemDeps tests on a
// developer machine that already has a git identity configured would have
// their warn output depend on that machine's config. The empty HOME also means
// this is, incidentally, the "git identity missing" state — see
// TestCheckSystemDeps_GitIdentitySet_NoWarn for the opposite.
//
// HOME/XDG_CONFIG_HOME/GIT_CONFIG_NOSYSTEM only neutralize git's GLOBAL and
// SYSTEM config tiers. `git config --get` (operations.GitIdentityDetail,
// doctor_cmd.go) always calls with dir="", so the child process inherits
// this TEST BINARY's cwd — which, absent this Chdir, is somewhere inside the
// ctxloom repo checkout. Git resolves LOCAL config by walking up from cwd to
// the nearest .git (for a linked worktree, that's the repo's shared common
// dir, .git/config — outranks global regardless of HOME), so a checkout
// that has ever had `user.name`/`user.email` set locally (directly, or via
// any linked worktree sharing that common dir) leaks straight through this
// "isolation" and the test's outcome depends on ambient host state again.
// Chdir-ing into a bare temp dir with no .git anywhere above it removes the
// local tier entirely, so only the global config this function controls
// (or, for TestCheckSystemDeps_GitIdentitySet_NoWarn, writes deliberately)
// is ever visible.
func isolateGitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	chdir(t, t.TempDir()) // outside any .git — kills the LOCAL config tier
}

// TestCheckSystemDeps_GitMissing_FailsLoud pins the new hard-block gate: a
// machine with no git on PATH must fail loud, naming git and a fix, BEFORE
// init ever reaches the clone step that would otherwise surface a raw,
// unguided "executable file not found" error.
func TestCheckSystemDeps_GitMissing_FailsLoud(t *testing.T) {
	isolateGitEnv(t)
	t.Setenv("PATH", t.TempDir()) // empty: no git, no docker/podman

	err := checkSystemDeps()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git")
	assert.Contains(t, err.Error(), "ctxloom init", "the fix must tell the user to re-run init")
}

// TestCheckSystemDeps_GitPresent_MissingExtrasWarnButDoNotBlock: with git on
// PATH but a container runtime and git identity absent, checkSystemDeps must
// still succeed (nil) — both are informational-only, needed by LATER phases,
// not by PRIME itself — while still surfacing a warning for each so the user
// sees the full picture up front. ssh-keygen is absent too and must not be
// mentioned: ctxloom has no use for it.
func TestCheckSystemDeps_GitPresent_MissingExtrasWarnButDoNotBlock(t *testing.T) {
	isolateGitEnv(t) // no host git config
	dir := fakeBinDir(t, "git")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps()
	})

	require.NoError(t, err, "a missing container runtime must not block init")
	assert.NotContains(t, stderr, "ssh-keygen", "ssh-keygen is not a dependency and must never be warned about")
	assert.Contains(t, stderr, "container runtime")
	assert.Contains(t, stderr, "git commit identity not fully set", "a missing git identity must warn too, informational-only like the others")
	assert.Contains(t, stderr, "user.name")
	assert.Contains(t, stderr, "user.email")
}

// TestCheckSystemDeps_AllPresent_Succeeds is the control case: with git on
// PATH the gate passes — this only pins that having it present never itself
// trips an error.
func TestCheckSystemDeps_AllPresent_Succeeds(t *testing.T) {
	isolateGitEnv(t)
	dir := fakeBinDir(t, "git")
	t.Setenv("PATH", dir)

	err := checkSystemDeps()
	require.NoError(t, err)
}

// TestCheckSystemDeps_GitIdentitySet_NoWarn proves warnIfGitIdentityMissing
// stays silent when BOTH user.name and user.email resolve — a real,
// isolated ~/.gitconfig (not the host's) written into isolateGitEnv's
// fresh HOME.
func TestCheckSystemDeps_GitIdentitySet_NoWarn(t *testing.T) {
	isolateGitEnv(t)
	home := os.Getenv("HOME")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[user]\n\tname = Ben\n\temail = ben@abbitt.me\n"), 0644))
	dir := fakeBinDir(t, "git")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps()
	})

	require.NoError(t, err)
	assert.NotContains(t, stderr, "git commit identity not fully set", "a fully resolved identity must never warn")
}
