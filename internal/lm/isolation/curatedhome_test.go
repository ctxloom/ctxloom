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

// TestProvisionCuratedHome_AllowlistIsExactlyGitconfigAndSSH locks the
// allowlist scope: extending it is a deliberate decision (a concrete
// breakage), never speculative widening. netrc/npmrc/gnupg carry plaintext
// tokens of their own and must NEVER be added implicitly.
func TestProvisionCuratedHome_AllowlistIsExactlyGitconfigAndSSH(t *testing.T) {
	assert.ElementsMatch(t, []string{".gitconfig", ".ssh"}, curatedHomeAllowlist)
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

// TestWorktree_Antigravity_AuthNotIsolatedFindingFires pins the LOUD,
// non-fatal finding: host+worktree antigravity proceeds (no refusal), but
// emits a warning naming config/session isolated vs. auth NOT isolated. This
// must NOT be a strictness.Finding (see curatedHomeAuthFinding's doc) — a
// partial lever must never read as isolation in the same pass/fail signal a
// dropped container boundary uses.
func TestWorktree_Antigravity_AuthNotIsolatedFindingFires(t *testing.T) {
	resetStrictness(t)
	withFakeHome(t)
	buf := captureWarnings(t)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "antigravity").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	out := buf.String()
	assert.Contains(t, out, "agent-a")
	assert.Contains(t, out, "CONFIGURATION and SESSION STATE are isolated")
	assert.Contains(t, out, "AUTHENTICATION IS NOT")
	assert.Contains(t, out, "runtime:container")

	// The finding is informational only: strict mode must NOT collect it as
	// a fatal ClassIsolation finding, and the run must NOT be refused (this
	// PrepareWorkspace call already returned err == nil above).
	assert.Empty(t, strictness.All(), "the auth-not-isolated finding must never be folded into the strictness pass/fail signal")
}

// TestWorktree_Antigravity_AuthNotIsolatedFindingFiresEvenDegraded: the
// finding is plain informational output, not gated by --degraded (which only
// suppresses STRICTNESS findings). A user who explicitly degraded still
// needs to know auth isn't isolated.
func TestWorktree_Antigravity_AuthNotIsolatedFindingFiresEvenDegraded(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	withFakeHome(t)
	buf := captureWarnings(t)

	common := t.TempDir()
	f := &git.Fake{CommonDirValue: common}
	ws, err := NewWorktree(f, "antigravity").PrepareWorkspace(context.Background(), "/proj", "agent-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	assert.Contains(t, buf.String(), "AUTHENTICATION IS NOT")
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
