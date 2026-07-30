package isolation

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureWarnings redirects clidiag's stderr-flavored helpers (Warn/WarnOnce)
// to a buffer for the duration of the test and returns it, so a test can
// assert on the LOUD non-fatal finding text without touching strictness (the
// finding is deliberately NOT a strictness.Finding — see
// curatedHomeAuthFinding's doc).
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// --- provisionCuratedHome: the symlink-allowlist mechanism itself ---------

// TestProvisionCuratedHome_SymlinksAllowlistedDotfiles pins the core payload:
// the curated HOME is a real directory, and ~/.gitconfig + ~/.ssh land inside
// it as SYMLINKS (never copies) resolving back to the exact host files/dirs.
func TestProvisionCuratedHome_SymlinksAllowlistedDotfiles(t *testing.T) {
	hostHome := withFakeHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte("[user]\n\tname = Test\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(hostHome, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, ".ssh", "id_ed25519"), []byte("fake-private-key"), 0o600))

	home := filepath.Join(t.TempDir(), "curated-home")
	require.NoError(t, provisionCuratedHome(home))

	// The curated HOME dir itself exists at 0700 (holds engine config/session
	// state, same convention as every other scratch dir in this package).
	info, err := os.Stat(home)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	// .gitconfig is a SYMLINK, not a copy, resolving back to the host file.
	gitconfigLink := filepath.Join(home, ".gitconfig")
	target, err := os.Readlink(gitconfigLink)
	require.NoError(t, err, "~/.gitconfig must be a symlink, not a copy")
	assert.Equal(t, filepath.Join(hostHome, ".gitconfig"), target)
	content, err := os.ReadFile(gitconfigLink)
	require.NoError(t, err)
	assert.Equal(t, "[user]\n\tname = Test\n", string(content), "the symlink resolves to the real content")

	// .ssh is a SYMLINK to the host dir, not a duplicated tree — the private
	// key never leaves ~/.ssh's own permissions.
	sshLink := filepath.Join(home, ".ssh")
	sshTarget, err := os.Readlink(sshLink)
	require.NoError(t, err, "~/.ssh must be a symlink, not a copied tree")
	assert.Equal(t, filepath.Join(hostHome, ".ssh"), sshTarget)
	keyContent, err := os.ReadFile(filepath.Join(home, ".ssh", "id_ed25519"))
	require.NoError(t, err)
	assert.Equal(t, "fake-private-key", string(keyContent))
}

// TestProvisionCuratedHome_MissingDotfilesSkippedNotDangling pins the
// absence-handling contract: a host with no ~/.gitconfig and no ~/.ssh gets
// NO symlinks for them at all — never a dangling link. A missing gitconfig
// is a legitimate host state, not an error.
func TestProvisionCuratedHome_MissingDotfilesSkippedNotDangling(t *testing.T) {
	withFakeHome(t) // empty fake HOME — neither dotfile exists

	home := filepath.Join(t.TempDir(), "curated-home")
	require.NoError(t, provisionCuratedHome(home))

	_, err := os.Lstat(filepath.Join(home, ".gitconfig"))
	assert.True(t, os.IsNotExist(err), "no dangling .gitconfig symlink when the host has none")
	_, err = os.Lstat(filepath.Join(home, ".ssh"))
	assert.True(t, os.IsNotExist(err), "no dangling .ssh symlink when the host has none")
}

// TestProvisionCuratedHome_PartialPresenceLinksOnlyWhatExists: only
// ~/.gitconfig present (no ~/.ssh) links only .gitconfig — the allowlist
// entries are independent, not all-or-nothing.
func TestProvisionCuratedHome_PartialPresenceLinksOnlyWhatExists(t *testing.T) {
	hostHome := withFakeHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte("x"), 0o644))

	home := filepath.Join(t.TempDir(), "curated-home")
	require.NoError(t, provisionCuratedHome(home))

	assert.FileExists(t, filepath.Join(home, ".gitconfig"))
	_, err := os.Lstat(filepath.Join(home, ".ssh"))
	assert.True(t, os.IsNotExist(err))
}

// TestProvisionCuratedHome_WarnsOnUnresolvableHostHome pins half of U062-F03:
// an os.UserHomeDir error used to be discarded with NO warning at all — the
// caller then points $HOME at the curated dir (still empty) and reports
// success. It must now be loud, even though provisioning still succeeds
// (an unresolvable host HOME is not itself a reason to fail the whole run).
func TestProvisionCuratedHome_WarnsOnUnresolvableHostHome(t *testing.T) {
	orig := hostHomeDir
	hostHomeDir = func() (string, error) { return "", assert.AnError }
	t.Cleanup(func() { hostHomeDir = orig })
	buf := captureWarnings(t)

	home := filepath.Join(t.TempDir(), "curated-home")
	require.NoError(t, provisionCuratedHome(home))
	assert.Contains(t, buf.String(), "could not resolve the host HOME", "an unresolvable host HOME must be loud, not silent")
}

// TestProvisionCuratedHome_WarnsOnSymlinkFailure pins the other half of
// U062-F03: os.Symlink failing for an allowlisted entry (e.g. the
// destination somehow already exists) used to be discarded with no count
// and no warning at all. It must now name the entry that failed.
func TestProvisionCuratedHome_WarnsOnSymlinkFailure(t *testing.T) {
	hostHome := withFakeHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte("x"), 0o644))
	buf := captureWarnings(t)

	home := filepath.Join(t.TempDir(), "curated-home")
	// Pre-create the destination as a plain (non-symlink) file so
	// os.Symlink fails with EEXIST for .gitconfig specifically.
	require.NoError(t, os.MkdirAll(home, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("pre-existing"), 0o644))

	require.NoError(t, provisionCuratedHome(home), "a per-entry symlink failure must not fail the whole provision")
	assert.Contains(t, buf.String(), ".gitconfig", "the warning must name the entry that failed to link")
}

// TestProvisionCuratedHome_AllowlistIsExactlyGitconfigAndSSH locks the
// allowlist scope: extending it is a deliberate decision (a concrete
// breakage), never speculative widening.
func TestProvisionCuratedHome_AllowlistIsExactlyGitconfigAndSSH(t *testing.T) {
	assert.ElementsMatch(t, []string{".gitconfig", ".ssh"}, curatedHomeAllowlist)
}

// TestProvisionCuratedHome_ExclusionIsMinimalismNotContainment is U062-F13's
// pin. The row read the allowlist as a confidentiality boundary and found it
// self-contradictory: all of ~/.ssh (every private key) admitted, while
// ~/.netrc is excluded for "carrying plaintext tokens". The premise is what is
// wrong. A curated HOME is only ever the HOME env var of a HOST process running
// as the SAME UID with no namespace (worktreeWorkspace.Env), so an excluded
// dotfile stays readable by absolute path regardless — omitting it withholds
// nothing, and admitting ~/.ssh grants nothing that was not already reachable.
//
// Measured here rather than argued, so that if a curated HOME ever DOES become
// a real boundary (a namespace, a different uid), this test stops being true and
// the allowlist's rationale has to be revisited deliberately.
func TestProvisionCuratedHome_ExclusionIsMinimalismNotContainment(t *testing.T) {
	hostHome := withFakeHome(t)
	netrc := filepath.Join(hostHome, ".netrc")
	require.NoError(t, os.WriteFile(netrc, []byte("machine example.com password hunter2\n"), 0o600))

	home := filepath.Join(t.TempDir(), "curated-home")
	require.NoError(t, provisionCuratedHome(home))

	_, err := os.Lstat(filepath.Join(home, ".netrc"))
	assert.True(t, os.IsNotExist(err), "an excluded dotfile is not linked into the curated HOME")

	// …and yet it is still fully readable from this very process, which is the
	// same uid the engine runs as. The exclusion contains nothing.
	got, err := os.ReadFile(netrc)
	require.NoError(t, err, "a same-uid host process reaches an excluded dotfile by absolute path")
	assert.Contains(t, string(got), "hunter2")
}

// TestCuratedHomeSpecs_OpencodeNotRegistered pins the dispatch decision
// directly at the registry level (sunny-saga): opencode has a REAL scoped-var
// lever (credentialSeedSpecs["opencode"], auth.go) — its whole HOME does not
// need to move, only XDG_CONFIG_HOME/XDG_DATA_HOME do. Registering it here
// too would be a mutual-exclusivity bug: PrepareWorkspace consults
// curatedHomeSpecs FIRST (`if spec, ok := curatedHomeSpecs[w.backend]; ok`),
// so a stray opencode entry here would silently shadow the scoped-var path
// this task wires up and route opencode through a blanket HOME override it
// does not need instead.
func TestCuratedHomeSpecs_OpencodeNotRegistered(t *testing.T) {
	_, ok := curatedHomeSpecs["opencode"]
	assert.False(t, ok, "opencode has a full scoped-var lever — it must never take the curated-HOME path")
}

// --- Worktree wiring: antigravity opts in, scoped-lever engines don't -----

// TestWorktree_Antigravity_HomeOverrideAndSymlinks is the end-to-end payload
// through the Policy: a host+worktree antigravity agent gets HOME pointed at
// a freshly provisioned curated home (not any of the scoped
// CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME vars — antigravity has none), and
// the allowlisted host dotfiles resolve inside it.
func TestWorktree_Antigravity_HomeOverrideAndSymlinks(t *testing.T) {
	resetStrictness(t)
	hostHome := withFakeHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte("[user]\n\tname = Test\n"), 0o644))

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "antigravity").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	env := WorkspaceEnv(ws)
	require.NotNil(t, env)
	home, ok := env["HOME"]
	require.True(t, ok, "antigravity's ONLY lever is HOME itself — Env() must set it")
	assert.NotEmpty(t, home)
	assert.NotContains(t, env, "CLAUDE_CONFIG_DIR")
	assert.NotContains(t, env, "CODEX_HOME")
	assert.NotContains(t, env, "KIRO_HOME")

	target, err := os.Readlink(filepath.Join(home, ".gitconfig"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(hostHome, ".gitconfig"), target)
}

// TestWorktree_Antigravity_CuratedHomeCleanedUpOnTeardown: Cleanup removes
// the curated HOME directory itself, but symlinks are UNLINKED, never
// followed — the host's real ~/.gitconfig and ~/.ssh must survive teardown
// untouched (os.RemoveAll never recurses through a symlink).
func TestWorktree_Antigravity_CuratedHomeCleanedUpOnTeardown(t *testing.T) {
	resetStrictness(t)
	hostHome := withFakeHome(t)
	require.NoError(t, os.WriteFile(filepath.Join(hostHome, ".gitconfig"), []byte("x"), 0o644))

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "antigravity").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)

	env := WorkspaceEnv(ws)
	home := env["HOME"]
	require.NotEmpty(t, home)

	require.NoError(t, ws.Cleanup())

	_, err = os.Stat(home)
	assert.True(t, os.IsNotExist(err), "the curated HOME dir itself is removed")
	assert.FileExists(t, filepath.Join(hostHome, ".gitconfig"), "the REAL host file must survive teardown — RemoveAll unlinks, never follows")
}

// TestWorktree_Antigravity_HostWorktreeRefused pins the CURRENT (fatal)
// contract, replacing the old non-fatal "AuthNotIsolatedFindingFires" test:
// a STANDALONE host+worktree antigravity run still succeeds mechanically
// (PrepareWorkspace itself never errors — the fail-loud gate is the CALLER's
// job, exactly like every other ClassIsolation finding in this package), but
// records a FATAL ClassIsolation finding naming BOTH escapes — auth (the
// keyring) and file writes (the cwd-ignoring global scratch) — and pointing
// at runtime:container. This finding is exactly what the choke owner
// (isolationGateErr) aborts a strict run on; --degraded is the only way
// through it (TestWorktree_Antigravity_HostWorktreeRefusalDowngradesWithDegraded
// below).
func TestWorktree_Antigravity_HostWorktreeRefused(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "antigravity").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err, "PrepareWorkspace itself still succeeds — the fail-loud gate is the CALLER's job")
	t.Cleanup(func() { _ = ws.Cleanup() })

	findings := strictness.All()
	require.Len(t, findings, 1, "a workspaceViable==false spec on the standalone host path must record exactly one fatal finding")
	f0 := findings[0]
	assert.Equal(t, strictness.ClassIsolation, f0.Class, "mirrors kiro's own fatal-unless-degraded posture")
	assert.Contains(t, f0.Message, "agent-a")
	assert.Contains(t, f0.Message, "AUTHENTICATION escapes it", "must name the auth escape (the OS session keyring)")
	assert.Contains(t, f0.Message, "FILE WRITES escape", "must name the file-write escape (cwd-ignoring global scratch)")
	assert.Contains(t, f0.Message, "antigravity-cli/scratch", "must name the actual escape path, not just gesture at it")
	assert.Contains(t, f0.Message, "runtime:container", "must point at the working alternative")
	assert.NotEmpty(t, f0.FixIt)
	assert.Contains(t, f0.FixIt, "runtime:container")
	assert.Contains(t, f0.FixIt, "--degraded")
}

// TestWorktree_Antigravity_HostWorktreeRefusalDowngradesWithDegraded: exactly
// like kiro's gateHomeVars gate, --degraded (CTXLOOM_DEGRADED=1) suppresses
// the FATAL finding entirely (strictness.record's degraded short-circuit) —
// the run proceeds with only config/session isolation, the user having
// explicitly accepted shared auth and shared global file writes.
func TestWorktree_Antigravity_HostWorktreeRefusalDowngradesWithDegraded(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	withFakeHome(t)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "antigravity").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	assert.Empty(t, strictness.All(), "--degraded downgrades the refusal to a plain warn-and-continue, exactly like kiro's gate")
}

// TestWorktree_Antigravity_ContainerWrappedKeepsWarnOnlyNoRefusal is the
// LOAD-BEARING container-exemption proof at the unit level: the IDENTICAL
// Worktree.PrepareWorkspace call, but through the worktree-in-container
// composition's exact construction (NewWorktree(...).forContainer(), what
// container_worktree.go's NewContainerWorktreeFor actually builds), must NOT
// record any ClassIsolation finding — it keeps the pre-existing loud-but-
// non-fatal curatedHomeAuthFinding warning, because a container's own
// mount/PID namespace contains both of antigravity's escapes. This pins the
// containerWrapped short-circuit directly, independent of the full
// Container/prepareBase plumbing (proven separately via
// TestNewContainerWorktreeFor_AntigravityWorktreeIsContainerWrapped in
// container_worktree_test.go).
func TestWorktree_Antigravity_ContainerWrappedKeepsWarnOnlyNoRefusal(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)
	buf := captureWarnings(t)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	// The exact construction container_worktree.go uses to wrap a Worktree
	// for the {workspace: worktree, runtime: container} composition.
	ws, err := NewWorktree(f, "antigravity").forContainer().PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	assert.Empty(t, strictness.All(), "a container-wrapped Worktree must NEVER record the host-only fatal refusal")
	assert.Contains(t, buf.String(), "AUTHENTICATION IS NOT", "the pre-existing non-fatal warning still fires, unchanged")
}

// TestWorktree_ScopedLeverEngines_NoHomeOverride is the negative space this
// change must not touch: claude-code/codex/kiro/opencode each have a scoped
// CONFIG_DIR-style (or XDG) lever, so worktree.go deliberately prefers that
// over a blanket $HOME override (it would strip ~/.gitconfig/ssh identity
// the worktree still needs for git itself — see provisionConfigHome's doc).
// opencode is included here as the direct proof for sunny-saga: it must take
// the SCOPED-VAR path (credentialSeedSpecs), never the curated-HOME path —
// curatedhome.go's registry doc explicitly calls out that opencode must NOT
// be wired there speculatively, since it has a real scoped lever. None of
// these backends may get a "HOME" key in Env(), and none of them fire the
// curated-home auth-not-isolated finding.
func TestWorktree_ScopedLeverEngines_NoHomeOverride(t *testing.T) {
	for _, tc := range []struct {
		backend string
		envVar  string
	}{
		{"claude-code", "CLAUDE_CONFIG_DIR"},
		{"codex", "CODEX_HOME"},
		{"kiro", "KIRO_HOME"},
		{"opencode", "XDG_DATA_HOME"},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			resetStrictness(t)
			withFakeHome(t)
			t.Setenv("ANTHROPIC_API_KEY", "sk-test")
			t.Setenv("OPENAI_API_KEY", "sk-test")
			t.Setenv("KIRO_API_KEY", "sk-test")
			t.Setenv("OPENROUTER_API_KEY", "sk-test")
			buf := captureWarnings(t)

			common := t.TempDir()
			f := &git.Fake{CommonDirValue: common}
			ws, err := NewWorktree(f, tc.backend).PrepareWorkspace(context.Background(), "/proj", "agent-a")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ws.Cleanup() })

			env := WorkspaceEnv(ws)
			require.NotNil(t, env)
			assert.NotContains(t, env, "HOME", "%s has a scoped lever — no blanket HOME override", tc.backend)
			assert.Contains(t, env, tc.envVar)
			assert.NotContains(t, buf.String(), "AUTHENTICATION IS NOT", "%s must not fire the curated-home partial-lever finding", tc.backend)
		})
	}
}
