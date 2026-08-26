package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
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

// isolateSignKeyEnv makes both warnIfNoSignKey's and warnIfGitIdentityMissing's
// real agentkey.NewDiscoverer() calls hermetic: SSH_AUTH_SOCK empty (never
// dials the host's real ssh-agent) and HOME/XDG_CONFIG_HOME repointed at a
// fresh, empty temp dir with system config disabled (GIT_CONFIG_NOSYSTEM) so
// `git config --get user.signingkey`/`user.name`/`user.email` never see the
// host's real config either. Without this, checkSystemDeps tests on a
// developer machine that already has SSH commit signing AND a git identity
// configured (the exact population these features target) would have their
// warn output depend on that machine's config. The empty HOME also means
// this is, incidentally, the "git identity missing" state — see
// TestCheckSystemDeps_GitIdentitySet_NoWarn for the opposite.
//
// HOME/XDG_CONFIG_HOME/GIT_CONFIG_NOSYSTEM only neutralize git's GLOBAL and
// SYSTEM config tiers. `git config --get` (gitIdentityDetail,
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
func isolateSignKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Chdir(t.TempDir()) // outside any .git — kills the LOCAL config tier
}

// TestCheckSystemDeps_GitMissing_FailsLoud pins the new hard-block gate: a
// machine with no git on PATH must fail loud, naming git and a fix, BEFORE
// init ever reaches the clone step that would otherwise surface a raw,
// unguided "executable file not found" error.
func TestCheckSystemDeps_GitMissing_FailsLoud(t *testing.T) {
	isolateSignKeyEnv(t)
	t.Setenv("PATH", t.TempDir()) // empty: no git, no ssh-keygen, no docker/podman

	err := checkSystemDeps("claude-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git")
	assert.Contains(t, err.Error(), "ctxloom init", "the fix must tell the user to re-run init")
}

// TestCheckSystemDeps_GitPresent_MissingExtrasWarnButDoNotBlock: with git on
// PATH but ssh-keygen and a container runtime absent, checkSystemDeps must
// still succeed (nil) — those two are informational-only, needed by LATER
// phases, not by PRIME itself — while still surfacing a warning for each so
// the user sees the full picture up front.
func TestCheckSystemDeps_GitPresent_MissingExtrasWarnButDoNotBlock(t *testing.T) {
	isolateSignKeyEnv(t) // deterministic "no signing key" — no SSH_AUTH_SOCK, no host git config
	dir := fakeBinDir(t, "git")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps("claude-code")
	})

	require.NoError(t, err, "missing ssh-keygen/container runtime must not block init")
	assert.Contains(t, stderr, "ssh-keygen")
	assert.Contains(t, stderr, "recommended, not required", "the ssh-keygen warning must not imply it's a hard requirement")
	assert.NotContains(t, stderr, "you'll need it later to sign your own bundles", "must not claim signing needs ssh-keygen — it's pure Go and never execs it")
	assert.Contains(t, stderr, "container runtime")
	assert.Contains(t, stderr, "no signing key resolves", "an absent signing key must warn too, informational-only like the others")
	assert.Contains(t, stderr, "ctxloom review", "must say WHY: approving reviewed content needs a key too, not just publishing")
	assert.Contains(t, stderr, "ctxloom bundle sign", "must also name the publishing feature this gap affects")
	assert.Contains(t, stderr, "git commit identity not fully set", "a missing git identity must warn too, informational-only like the others")
	assert.Contains(t, stderr, "user.name")
	assert.Contains(t, stderr, "user.email")
	assert.Contains(t, stderr, "missing ACP adapter", "a missing claude-code-acp for the resolved engine must warn too")
	assert.Contains(t, stderr, "claude-code-acp")
	assert.Contains(t, stderr, "containerized agents", "must acknowledge container-runtime agents get the adapter from their image")
}

// TestCheckSystemDeps_AllPresent_Succeeds is the control case: with git,
// ssh-keygen, AND the resolved engine's ACP adapter all on PATH, the whole
// gate is silent — this only pins that having them present never itself
// trips an error.
func TestCheckSystemDeps_AllPresent_Succeeds(t *testing.T) {
	isolateSignKeyEnv(t)
	dir := fakeBinDir(t, "git", "ssh-keygen")
	writeFakeExecutable(t, dir, claude.ClaudeACPAdapter)
	t.Setenv("PATH", dir)

	err := checkSystemDeps("claude-code")
	require.NoError(t, err)
}

// TestCheckSystemDeps_SignKeyResolves_NoWarn proves warnIfNoSignKey stays
// silent when a signing key DOES resolve — mirroring
// TestCheckSystemDeps_AllPresent_Succeeds but with a hermetic ssh-agent (see
// startFakeSSHAgent, doctor_cmd_test.go) holding exactly one identity, the
// same "ssh-agent (sole identity)" step of the chain agentkey_test.go covers.
func TestCheckSystemDeps_SignKeyResolves_NoWarn(t *testing.T) {
	isolateSignKeyEnv(t)
	sock := startFakeSSHAgent(t, "ben@abbitt.me")
	t.Setenv("SSH_AUTH_SOCK", sock)
	dir := fakeBinDir(t, "git", "ssh-keygen")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps("claude-code")
	})

	require.NoError(t, err)
	assert.NotContains(t, stderr, "no signing key resolves", "a resolvable key must never warn")
}

// TestCheckSystemDeps_GitIdentitySet_NoWarn proves warnIfGitIdentityMissing
// stays silent when BOTH user.name and user.email resolve — a real,
// isolated ~/.gitconfig (not the host's) written into isolateSignKeyEnv's
// fresh HOME, mirroring TestCheckSystemDeps_SignKeyResolves_NoWarn's shape
// for the sibling check.
func TestCheckSystemDeps_GitIdentitySet_NoWarn(t *testing.T) {
	isolateSignKeyEnv(t)
	home := os.Getenv("HOME")
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[user]\n\tname = Ben\n\temail = ben@abbitt.me\n"), 0644))
	sock := startFakeSSHAgent(t, "ben@abbitt.me")
	t.Setenv("SSH_AUTH_SOCK", sock)
	dir := fakeBinDir(t, "git", "ssh-keygen")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps("claude-code")
	})

	require.NoError(t, err)
	assert.NotContains(t, stderr, "git commit identity not fully set", "a fully resolved identity must never warn")
}

// TestCheckSystemDeps_ACPAdapterPresent_NoWarn proves warnIfACPAdapterMissing
// stays silent when the resolved engine's ACP adapter IS on PATH — mirroring
// TestCheckSystemDeps_SignKeyResolves_NoWarn's shape for this sibling check.
func TestCheckSystemDeps_ACPAdapterPresent_NoWarn(t *testing.T) {
	isolateSignKeyEnv(t)
	dir := fakeBinDir(t, "git", "ssh-keygen")
	writeFakeExecutable(t, dir, claude.ClaudeACPAdapter)
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps("claude-code")
	})

	require.NoError(t, err)
	assert.NotContains(t, stderr, "missing ACP adapter", "a resolvable adapter must never warn")
}

// TestCheckSystemDeps_ACPAdapterEngineWithNoAdapter_NoWarn proves an engine
// with no separate ACP adapter (kiro speaks ACP natively) never warns here
// regardless of PATH.
func TestCheckSystemDeps_ACPAdapterEngineWithNoAdapter_NoWarn(t *testing.T) {
	isolateSignKeyEnv(t)
	dir := fakeBinDir(t, "git", "ssh-keygen")
	t.Setenv("PATH", dir)

	var err error
	stderr := captureStderr(t, func() {
		err = checkSystemDeps("kiro")
	})

	require.NoError(t, err)
	assert.NotContains(t, stderr, "missing ACP adapter", "kiro speaks ACP natively; there is no adapter to be missing")
}
