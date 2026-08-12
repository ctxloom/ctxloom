package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

const (
	codexHarpA = "ugly-icy-squid"
	codexHarpB = "brave-warm-otter"
)

// requireBytes reads path and fails unless it holds something — the empty-source
// guard. Without it a writer that produced zero bytes would satisfy every
// "contents are equal" assertion below by vacuity, which is this project's
// characteristic false green.
func requireBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err, "%s must exist", path)
	require.NotEmpty(t, data, "%s is empty; a byte comparison against it would be vacuous", path)
	return data
}

// TestSessionHome_IsTheSessionInstanceRoot pins codex's instance to the ONE
// location the per-session model names, derived from paths.SessionHomePath
// rather than a literal spelled twice. codex's own leaf (".codex") is appended
// by cellScopedCodexHome, which is why SessionHome returns the shared root:
// claude's "claude" and kiro's "kiro" hang off the very same directory.
func TestSessionHome_IsTheSessionInstanceRoot(t *testing.T) {
	want, err := paths.SessionHomePath(filepath.Join("/proj", paths.AppDirName), codexHarpA)
	require.NoError(t, err)

	got, err := SessionHome("/proj", codexHarpA)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, filepath.Join("/proj", ".ctxloom", "state", codexHarpA, "home", ConfigDirName), cellScopedCodexHome(got),
		"spelled out once, so a change to the layout cannot pass by agreeing with itself")
}

// TestSessionHome_IsPerSession: two concurrent sessions in one checkout get two
// CODEX_HOMEs, so neither reads the other's copied auth.json nor clobbers its
// generated config.toml (codex has no per-invocation config redirect at all).
func TestSessionHome_IsPerSession(t *testing.T) {
	a, err := SessionHome("/proj", codexHarpA)
	require.NoError(t, err)
	b, err := SessionHome("/proj", codexHarpB)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "the instance must be keyed by harp, not by project")
	assert.Contains(t, a, codexHarpA)
}

// TestSessionHome_RefusesAHarplessCaller is gate (b) at codex's own resolver:
// no session, no instance, and no shared fallback — a shared fallback is
// exactly the durable per-project home this model retired.
func TestSessionHome_RefusesAHarplessCaller(t *testing.T) {
	for _, bad := range []string{"", "..", "../.."} {
		got, err := SessionHome("/proj", bad)
		assert.Error(t, err, "SessionHome(harp=%q) must refuse", bad)
		assert.Empty(t, got, "a refused resolution must name nothing")
	}
}

// TestSessionHome_NeverTheRealHostHome: the instance lives under the PROJECT's
// .ctxloom/state tier, never the user's own ~/.codex. Asserted structurally so
// it holds without consulting a real HOME.
func TestSessionHome_NeverTheRealHostHome(t *testing.T) {
	got, err := SessionHome("proj", codexHarpA)
	require.NoError(t, err)
	assert.False(t, filepath.IsAbs(got), "a project-relative workDir must stay project-relative")
	assert.True(t, strings.HasPrefix(got, filepath.Join("proj", paths.AppDirName, paths.StateDir)),
		"the instance belongs to the state tier")
}

// TestSetup_SessionInstancePreSeedsTrustForTheWorkingDirectory pins the trust
// half of the policy. The instance is a home codex has never seen, so the
// `[projects."<abs cwd>"] trust_level = "trusted"` entry codex accumulates on
// its own is not in it — without a pre-seed codex re-prompts, or (under `codex
// exec`, which has nobody to ask) runs silently untrusted. The key is the
// WORKING DIRECTORY: codex keys trust by the cwd it runs in, so an entry naming
// the home would answer a question it never asks.
func TestSetup_SessionInstancePreSeedsTrustForTheWorkingDirectory(t *testing.T) {
	workDir := t.TempDir()
	instance, err := SessionHome(workDir, codexHarpA)
	require.NoError(t, err)

	b := NewCodex()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir: workDir,
		Env:     map[string]string{CodexHomeEnv: filepath.Join(instance, ConfigDirName)},
		Managed: &agent.ManagedConfig{Hooks: &wire.HooksConfig{}},
	}))

	absWork, err := filepath.Abs(workDir)
	require.NoError(t, err)
	assert.Equal(t, absWork, b.resolvedTrustAbsPath)

	cfg := requireBytes(t, filepath.Join(instance, ConfigDirName, ConfigFileName))
	assert.Contains(t, string(cfg), absWork, "the trust entry names the working directory")
	assert.Contains(t, string(cfg), "trust_level", "and grants it")
	assert.NotContains(t, string(cfg), cellScopedCodexHome(instance),
		"no trust entry may name the config HOME — codex keys trust by cwd")
}

// TestSetup_RealHostHomeIsNeverWritten is D2's cost stated as a guarantee: a
// run with no controlled home uses the user's real ~/.codex, and Setup writes
// NOTHING into it — no trust pre-seed, no credential copy, not even the
// directory. The delivery half of that (surfaces) is covered by the
// byte-identity invariant in tests/arch.
func TestSetup_RealHostHomeIsNeverWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test") // authenticate without an auth.json
	workDir := t.TempDir()

	b := NewCodex()
	_ = b.Setup(context.Background(), &agent.SetupRequest{WorkDir: workDir})

	assert.Equal(t, home, b.resolvedProjectDir)
	assert.Empty(t, b.resolvedTrustAbsPath, "ctxloom answers a trust prompt only for a home it created")

	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "Setup created something inside the user's real home: %v", entries)
}

// TestLegacyProjectHome_IsGone is D3 (ruled 2026-08-11: DROP IT). The
// pre-relocation <WorkDir>/.codex gets NO handling — no migration, no copy-in
// source, no symlink refusal. The machinery that moved it was written the same
// morning and its premise (a DURABLE destination to move into) no longer
// exists. This test is the standing statement that nothing reintroduced it: a
// run must not read, move, or create that directory.
func TestLegacyProjectHome_IsGone(t *testing.T) {
	workDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	legacy := filepath.Join(workDir, ConfigDirName)
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, ConfigFileName), []byte("model = 'o3'\n"), 0o644))
	before := requireBytes(t, filepath.Join(legacy, ConfigFileName))

	b := NewCodex()
	_ = b.Setup(context.Background(), &agent.SetupRequest{WorkDir: workDir})

	assert.Equal(t, string(before), string(requireBytes(t, filepath.Join(legacy, ConfigFileName))),
		"the legacy directory is inert: not read, not moved, not rewritten")
	instanceRoot := filepath.Join(workDir, paths.AppDirName, paths.StateDir)
	_, err := os.Stat(instanceRoot)
	assert.True(t, os.IsNotExist(err), "and nothing migrated it into the state tier")
}
