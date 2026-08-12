package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/kiro"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetEngineHomeStrictness gives each case its own findings state: several of
// these assert on whether a ClassIsolation finding was recorded, which a
// leftover from a sibling case would silently satisfy.
func resetEngineHomeStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// fakeHostHome points $HOME at a scratch directory and, when creds is
// non-empty, writes it as the host's ~/.claude/.credentials.json. Returns the
// home path. Every case that touches claude uses this so no test can read (or
// write) the developer's real credentials.
func fakeHostHome(t *testing.T, creds string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("KIRO_API_KEY", "")
	if creds != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(creds), 0o600))
	}
	return home
}

// hostCredentialFixture is a non-empty stand-in for a real
// ~/.claude/.credentials.json. Non-empty on purpose: a byte-for-byte comparison
// between two empty files proves nothing, and "exit 0 having written zero
// bytes" is this project's signature failure mode.
const hostCredentialFixture = `{"claudeAiOauth":{"accessToken":"seed-fixture-token","refreshToken":"seed-fixture-refresh"}}`

// t1 — an in-tree AGENT run for claude-code is handed CLAUDE_CONFIG_DIR at the
// project-scoped state home, and the host credential is really there, byte for
// byte, owner-only. The env var alone would be a half-truth: a controlled home
// claude cannot authenticate against is worse than no relocation at all.
func TestInTreeAgentHomeEnv_ClaudeGetsASeededControlledHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{
		Backend:    "claude-code",
		WorkDir:    workDir,
		ConfigHome: agents.ConfigHomeProject,
		Policy:     isolation.None{},
	})

	want := claude.InTreeConfigDir(workDir)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want}, got)

	seeded, err := os.ReadFile(filepath.Join(want, ".credentials.json"))
	require.NoError(t, err, "the controlled home must actually carry the seeded credential")
	assert.Equal(t, hostCredentialFixture, string(seeded), "seeded bytes must match the host source exactly")
	require.NotEmpty(t, seeded, "empty-source guard: the fixture must carry bytes")

	info, err := os.Stat(filepath.Join(want, ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a seeded credential is owner-only")

	assert.Empty(t, strictness.All(), "a fully seeded home records no finding")
}

// t1b — the host's own ~/.claude is READ and never written. There is no
// migration on this axis (nothing has ever lived at the new path); the human's
// home is somebody else's property.
func TestInTreeAgentHomeEnv_NeverWritesTheRealHostHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	home := fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	before, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)

	InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})

	after, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the host credential's bytes changed")

	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "seeding added files to the human's own ~/.claude")
}

// t2 — kiro's in-tree agent home. KIRO_HOME points at the state home and the
// directory EXISTS (kiro has no seed step to create it as a side effect), and
// XDG_DATA_HOME is deliberately NOT contributed: kiro's credentials live in a
// global sqlite there, and relocating it with nothing to seed would strand the
// agent logged out.
func TestInTreeAgentHomeEnv_KiroGetsAFreshControlledHomeAndNoCredentialRelocation(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, "")
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{
		Backend:    "kiro",
		WorkDir:    workDir,
		ConfigHome: agents.ConfigHomeProject,
		Policy:     isolation.None{},
	})

	want := kiro.InTreeHome(workDir)
	assert.Equal(t, map[string]string{kiro.HomeEnv: want}, got)
	assert.NotContains(t, got, kiro.XDGDataHomeEnv, "relocating kiro's credential store in-tree would log the agent out")

	info, err := os.Stat(want)
	require.NoError(t, err, "the controlled home directory must exist before kiro is launched at it")
	assert.True(t, info.IsDir())
	assert.Empty(t, strictness.All(), "kiro needs no credential seed, so nothing fails loud")
}

// t3 — THE SCOPING RULE, no-binding half. A run with no agent binding at all
// (InTreeAgentHome.ConfigHome == "", the human's own session) keeps the REAL
// host home, and nothing is created in the tree. Yanking a human's own
// ~/.claude memory, plugins and settings out from under their interactive
// session is a regression, not isolation.
func TestInTreeAgentHomeEnv_OwnerSessionKeepsTheRealHostHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	for _, backend := range []string{"claude-code", "kiro"} {
		got := InTreeAgentHomeEnv(InTreeAgentHome{
			Backend:    backend,
			WorkDir:    workDir,
			ConfigHome: "",
			Policy:     isolation.None{},
		})
		assert.Nil(t, got, "%s: an unbound owner session must be handed no config-home override", backend)
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state", "engines"),
		"an owner session must not even create the controlled home")
}

// t3b — THE SCOPING RULE, undeclared-binding half. An AGENT-BOUND run whose
// binding never declares config_home resolves to agents.ConfigHomeHost
// (operations.ResolveConfigHome's default) — MUTATION TARGET m1: flip that
// default to agents.ConfigHomeProject and this goes red, because a bound-but-
// undeclared run would then get a controlled home it never asked for.
func TestInTreeAgentHomeEnv_UndeclaredBindingKeepsTheRealHostHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	for _, backend := range []string{"claude-code", "kiro"} {
		declared, err := ResolveConfigHome("") // what an undeclared binding resolves to
		require.NoError(t, err)
		got := InTreeAgentHomeEnv(InTreeAgentHome{
			Backend:    backend,
			WorkDir:    workDir,
			ConfigHome: declared,
			Policy:     isolation.None{},
		})
		assert.Nil(t, got, "%s: an agent-bound run with an UNDECLARED config_home must still keep the real host home", backend)
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state", "engines"))
}

// t3c — THE SCOPING RULE, declared-host half. A binding that EXPLICITLY
// declares config_home: host reads identically to an undeclared one on this
// axis — MUTATION TARGET m2: a bug that ignored a declared "host" value
// (treating any resolved agent binding as project) would make this red while
// t3b (the undeclared case) could stay green, so the two are pinned
// separately even though they assert the same outcome.
func TestInTreeAgentHomeEnv_DeclaredHostKeepsTheRealHostHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{
		Backend:    "claude-code",
		WorkDir:    workDir,
		ConfigHome: agents.ConfigHomeHost,
		Policy:     isolation.None{},
	})
	assert.Nil(t, got, "a binding that DECLARES config_home: host must keep the real host home")
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state", "engines"))
}

// t4 — PRECEDENCE. The worktree axis sets these vars itself (through
// isolation's Env()), and a user's own `--env CLAUDE_CONFIG_DIR=...` rides the
// same map. Either way an already-set value WINS: this contribution fills gaps,
// it never overrides.
func TestInTreeAgentHomeEnv_AlreadySetVarWins(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{
		Backend:    "claude-code",
		WorkDir:    workDir,
		ConfigHome: agents.ConfigHomeProject,
		Policy:     isolation.None{},
		Env:        map[string]string{claude.ConfigDirEnv: "/somewhere/isolation/put/it"},
	})
	assert.Nil(t, got, "an isolation- or user-provided config home must not be overridden")
	assert.NoDirExists(t, claude.InTreeConfigDir(workDir), "the losing arm must not create its home either")

	gotKiro := InTreeAgentHomeEnv(InTreeAgentHome{
		Backend:    "kiro",
		WorkDir:    workDir,
		ConfigHome: agents.ConfigHomeProject,
		Policy:     isolation.None{},
		Env:        map[string]string{kiro.HomeEnv: "/somewhere/isolation/put/it"},
	})
	assert.Nil(t, gotKiro)
}

// t5 — an ISOLATED policy contributes nothing on either axis. A container run's
// fresh in-container $HOME is already the controlled home; a worktree run's
// per-agent config home is already provisioned and seeded by isolation itself.
// Contributing an in-tree path to either would point the engine at a directory
// that does not exist inside the boundary.
func TestInTreeAgentHomeEnv_IsolatedPoliciesContributeNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	policies := map[string]isolation.Policy{
		"container": isolation.Container{},
		"worktree":  isolation.NewWorktree(&git.Fake{}, "claude-code"),
	}
	for name, policy := range policies {
		for _, backend := range []string{"claude-code", "kiro"} {
			got := InTreeAgentHomeEnv(InTreeAgentHome{
				Backend:    backend,
				WorkDir:    workDir,
				ConfigHome: agents.ConfigHomeProject,
				Policy:     policy,
			})
			assert.Nil(t, got, "%s/%s: the in-tree contribution must not fire off the in-tree axis", name, backend)
		}
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state", "engines"))
}

// A nil Policy is the injected-Factory / no-isolation-resolved path (oneshot's
// test seam). Treat it as "no in-tree axis was resolved" and contribute
// nothing, rather than assuming none: guessing the axis is how an engine ends
// up pointed at a home the boundary cannot see.
func TestInTreeAgentHomeEnv_NilPolicyContributesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	assert.Nil(t, InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject}))
}

// codex is NOT in this registry, and must not be: it relocates CODEX_HOME on
// EVERY axis through its own resolveCodexProjectDir, seeds itself in Setup, and
// pre-seeds workspace trust. A second contributor here would fight it.
func TestInTreeAgentHomeEnv_CodexIsNotContributedHere(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "codex", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	assert.Nil(t, got, "codex owns its own home resolution on every axis")
	assert.NoDirExists(t, codex.StateHome(workDir))
}

// An engine with no in-tree home policy at all (opencode — deferred pending the
// XDG_CONFIG_HOME blast-radius decision) contributes nothing, silently and by
// design.
func TestInTreeAgentHomeEnv_UnregisteredBackendContributesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)

	assert.Nil(t, InTreeAgentHomeEnv(InTreeAgentHome{Backend: "opencode", WorkDir: t.TempDir(), ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}}))
	assert.Nil(t, InTreeAgentHomeEnv(InTreeAgentHome{Backend: "mock", WorkDir: t.TempDir(), ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}}))
}

// Nothing to seed is FAIL-LOUD, never a silent relocation: with neither
// ANTHROPIC_API_KEY nor a host credential file, pointing claude at an empty
// controlled home would strand the agent logged out. Record the ClassIsolation
// finding the choke owner aborts on, and contribute NOTHING — so a --degraded
// run falls back to the host home it used before this policy existed, instead
// of launching against a home that cannot authenticate.
func TestInTreeAgentHomeEnv_NothingToSeedFailsLoudAndContributesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, "") // no ~/.claude at all, no API key
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	assert.Nil(t, got, "a home that cannot be authenticated must not be handed to the engine")

	found := strictness.All()
	require.Len(t, found, 1, "an unauthenticatable controlled home must fail loud")
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
	assert.Contains(t, found[0].Message, "ANTHROPIC_API_KEY")
	assert.NotEmpty(t, found[0].FixIt, "a finding without a fix-it leaves the user stuck")
}

// The API-key path: auth rides the environment, so there is nothing to seed and
// nothing to fail about — the controlled home is still handed over, and it
// exists.
func TestInTreeAgentHomeEnv_ApiKeyAuthenticatesAFreshControlledHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: claude.InTreeConfigDir(workDir)}, got)
	assert.DirExists(t, claude.InTreeConfigDir(workDir), "the home must exist even when nothing was seeded into it")
	assert.Empty(t, strictness.All())
}

// The state tier, not the cache tier: this home holds seeded credentials and a
// user's hand-edits, so it must sit where the single .ctxloom/state ignore rule
// covers it and where no `deps pull` or cache wipe can reconstruct-and-clobber
// it. Pinned against paths.EngineStateHome through each engine's own helper, so
// the run path and any future static writer resolve one root.
func TestInTreeAgentHomeEnv_ResolvesThroughTheEngineStateHomeHelpers(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	claudeEnv := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	kiroEnv := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "kiro", WorkDir: workDir, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})

	stateDir := filepath.Join(workDir, ".ctxloom", "state", "engines")
	assert.Equal(t, filepath.Join(stateDir, "claude-code", "claude"), claudeEnv[claude.ConfigDirEnv])
	assert.Equal(t, filepath.Join(stateDir, "kiro", "kiro"), kiroEnv[kiro.HomeEnv])
	assert.NotContains(t, claudeEnv[claude.ConfigDirEnv], filepath.Join(".ctxloom", "cache"))
	assert.NotContains(t, kiroEnv[kiro.HomeEnv], filepath.Join(".ctxloom", "cache"))
}
