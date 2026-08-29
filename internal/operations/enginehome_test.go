package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	// parked_engines: codex/kiro are out of the default build. Their positive
	// (a real controlled home IS produced) test cases below are commented out
	// with them; the negative (backend contributes nothing) cases that used
	// their names as examples still pass — now because the backend is
	// unregistered rather than for the reason they were written to prove, a
	// narrowing worth knowing about but not worth a rewrite for a parked
	// engine.
	// "github.com/ctxloom/ctxloom/internal/codex"
	"github.com/ctxloom/ctxloom/internal/git"
	// "github.com/ctxloom/ctxloom/internal/kiro"
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

// codexCredentialFixture is the codex analog of hostCredentialFixture, and
// non-empty for the same reason.
const codexCredentialFixture = `{"tokens":{"access_token":"codex-seed-fixture"}}`

// fakeCodexHostHome writes a host ~/.codex/auth.json under whatever $HOME is
// currently set to (fakeHostHome, or this test's own), so the codex copy-in has
// a real source and OPENAI_API_KEY is cleared so the envTrigger cannot mask a
// missing copy.
func fakeCodexHostHome(t *testing.T) {
	t.Helper()
	if os.Getenv("HOME") == "" {
		t.Setenv("HOME", t.TempDir())
	}
	t.Setenv("OPENAI_API_KEY", "")
	dir := filepath.Join(os.Getenv("HOME"), ".codex")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(codexCredentialFixture), 0o600))
}

// mustClaudeInstance / mustKiroInstance / mustCodexInstance resolve one
// session's instance through the owning engine package's OWN helper, so these
// assertions cannot drift from the resolution the production path uses.
func mustClaudeInstance(t *testing.T, workDir, harp string) string {
	t.Helper()
	dir, err := claude.SessionConfigDir(workDir, harp)
	require.NoError(t, err)
	return dir
}

// parked_engines: mustKiroInstance/mustCodexInstance served the kiro/codex
// positive-path cases below, all commented out with those packages.
//
// func mustKiroInstance(t *testing.T, workDir, harp string) string {
// 	t.Helper()
// 	dir, err := kiro.SessionHome(workDir, harp)
// 	require.NoError(t, err)
// 	return dir
// }
//
// func mustCodexInstance(t *testing.T, workDir, harp string) string {
// 	t.Helper()
// 	root, err := codex.SessionHome(workDir, harp)
// 	require.NoError(t, err)
// 	return filepath.Join(root, codex.ConfigDirName)
// }

// The two session names every case here keys its instances by.
const (
	harpA = "ugly-icy-squid"
	harpB = "brave-warm-otter"
)

// hostCredentialFixture is a non-empty stand-in for a real
// ~/.claude/.credentials.json. Non-empty on purpose: a byte-for-byte comparison
// between two empty files proves nothing, and "exit 0 having written zero
// bytes" is this project's signature failure mode.
const hostCredentialFixture = `{"claudeAiOauth":{"accessToken":"seed-fixture-token","refreshToken":"seed-fixture-refresh"}}`

// t1 — an in-tree AGENT run for claude-code is handed CLAUDE_CONFIG_DIR at the
// project-scoped state home, and the host credential is really there,
// owner-only — but ACCESS-TOKEN-ONLY (easiest-stomp): claude's projector strips
// the single-use rotating refresh token as it seeds, so a disposable home can
// never rotate and invalidate the human's own login. The env var alone would be
// a half-truth: a controlled home claude cannot authenticate against is worse
// than no relocation at all.
func TestInTreeAgentHomeEnv_ClaudeGetsASeededControlledHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	got := InTreeAgentHomeEnv(InTreeAgentHome{
		Backend:    "claude-code",
		WorkDir:    workDir,
		Harp:       harpA,
		ConfigHome: agents.ConfigHomeProject,
		Policy:     isolation.None{},
	})

	want := mustClaudeInstance(t, workDir, harpA)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want}, got)

	seeded, err := os.ReadFile(filepath.Join(want, ".credentials.json"))
	require.NoError(t, err, "the controlled home must actually carry the seeded credential")
	require.NotEmpty(t, seeded, "empty-source guard: the fixture must carry bytes")
	assert.Contains(t, string(seeded), "seed-fixture-token", "the access token is seeded so the home authenticates")
	assert.NotContains(t, string(seeded), "seed-fixture-refresh", "the single-use refresh token is stripped from the copy")
	assert.NotContains(t, string(seeded), "refreshToken", "no refresh-token field survives into the copy")

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

	InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})

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
// parked_engines: kiro is out of the default build; InTreeAgentHomeFor("kiro")
// now finds no descriptor and this positive assertion would fail. Comes back
// with the package.
//
// func TestInTreeAgentHomeEnv_KiroGetsAFreshControlledHomeAndNoCredentialRelocation(t *testing.T) {
// 	resetEngineHomeStrictness(t)
// 	fakeHostHome(t, "")
// 	workDir := t.TempDir()
//
// 	got := InTreeAgentHomeEnv(InTreeAgentHome{
// 		Backend:    "kiro",
// 		WorkDir:    workDir,
// 		Harp:       harpA,
// 		ConfigHome: agents.ConfigHomeProject,
// 		Policy:     isolation.None{},
// 	})
//
// 	want := mustKiroInstance(t, workDir, harpA)
// 	assert.Equal(t, map[string]string{kiro.HomeEnv: want}, got)
// 	assert.NotContains(t, got, kiro.XDGDataHomeEnv, "relocating kiro's credential store in-tree would log the agent out")
//
// 	info, err := os.Stat(want)
// 	require.NoError(t, err, "the controlled home directory must exist before kiro is launched at it")
// 	assert.True(t, info.IsDir())
// 	assert.Empty(t, strictness.All(), "kiro needs no credential seed, so nothing fails loud")
// }

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
			Harp:       harpA,
			ConfigHome: "",
			Policy:     isolation.None{},
		})
		assert.Nil(t, got, "%s: an unbound owner session must be handed no config-home override", backend)
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"),
		"an owner session must not even create the instance root")
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
			Harp:       harpA,
			ConfigHome: declared,
			Policy:     isolation.None{},
		})
		assert.Nil(t, got, "%s: an agent-bound run with an UNDECLARED config_home must still keep the real host home", backend)
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"))
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
		Harp:       harpA,
		ConfigHome: agents.ConfigHomeHost,
		Policy:     isolation.None{},
	})
	assert.Nil(t, got, "a binding that DECLARES config_home: host must keep the real host home")
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"))
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
		Harp:       harpA,
		ConfigHome: agents.ConfigHomeProject,
		Policy:     isolation.None{},
		Env:        map[string]string{claude.ConfigDirEnv: "/somewhere/isolation/put/it"},
	})
	assert.Nil(t, got, "an isolation- or user-provided config home must not be overridden")
	assert.NoDirExists(t, mustClaudeInstance(t, workDir, harpA), "the losing arm must not create its home either")

	// parked_engines: kiro's already-set-var-wins half is commented out, not
	// merely narrowed — kiro is unregistered now, so InTreeAgentHomeFor
	// returns !ok before the already-set-var check this half exists to
	// prove is ever reached; keeping it would pass for the wrong reason.
	// gotKiro := InTreeAgentHomeEnv(InTreeAgentHome{
	// 	Backend:    "kiro",
	// 	WorkDir:    workDir,
	// 	Harp:       harpA,
	// 	ConfigHome: agents.ConfigHomeProject,
	// 	Policy:     isolation.None{},
	// 	Env:        map[string]string{kiro.HomeEnv: "/somewhere/isolation/put/it"},
	// })
	// assert.Nil(t, gotKiro)
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
				Harp:       harpA,
				ConfigHome: agents.ConfigHomeProject,
				Policy:     policy,
			})
			assert.Nil(t, got, "%s/%s: the in-tree contribution must not fire off the in-tree axis", name, backend)
		}
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"))
}

// A nil Policy is the injected-Factory / no-isolation-resolved path (oneshot's
// test seam). Treat it as "no in-tree axis was resolved" and contribute
// nothing, rather than assuming none: guessing the axis is how an engine ends
// up pointed at a home the boundary cannot see.
func TestInTreeAgentHomeEnv_NilPolicyContributesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	assert.Nil(t, InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject}))
}

// D2 (RULED) — codex IS contributed here now, and this is the ONE
// decision point for all three engines. codex used to own its home resolution
// on every axis and relocate unconditionally; a per-session, DISPOSABLE
// instance made that a taking (token refreshes, accumulated trust, session
// state, gone every session), so the decision moved here, where config_home is
// read.
//
// MUTATION TARGET m3: revert codex to unconditional relocation (drop this
// descriptor entry) and this goes red — the instance would never be contributed
// and codex would relocate itself instead.
// parked_engines: codex is out of the default build; InTreeAgentHomeFor("codex")
// now finds no descriptor and this positive assertion would fail. Comes back
// with the package.
//
// func TestInTreeAgentHomeEnv_CodexReadsConfigHomeLikeTheOthers(t *testing.T) {
// 	resetEngineHomeStrictness(t)
// 	fakeCodexHostHome(t)
// 	workDir := t.TempDir()
//
// 	got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "codex", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
// 	want := mustCodexInstance(t, workDir, harpA)
// 	assert.Equal(t, map[string]string{codex.CodexHomeEnv: want}, got,
// 		"a `config_home: project` binding gets this session's own CODEX_HOME")
// 	assert.DirExists(t, want)
//
// 	seeded, err := os.ReadFile(filepath.Join(want, codex.AuthFileName))
// 	require.NoError(t, err, "the instance must carry the copied credential")
// 	assert.Equal(t, codexCredentialFixture, string(seeded), "copied bytes must match the host source exactly")
// 	require.NotEmpty(t, seeded, "empty-source guard: the fixture must carry bytes")
// }

// The other half of D2: no binding, an undeclared binding, or an explicit
// `host` all keep the user's REAL ~/.codex — nothing is contributed and nothing
// is created in the tree. This is the case codex's unconditional relocation
// used to break.
func TestInTreeAgentHomeEnv_CodexHostAndUnboundKeepTheRealHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeCodexHostHome(t)
	workDir := t.TempDir()

	declared, err := ResolveConfigHome("")
	require.NoError(t, err)
	for name, configHome := range map[string]string{"no binding": "", "undeclared": declared, "host": agents.ConfigHomeHost} {
		got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "codex", WorkDir: workDir, Harp: harpA, ConfigHome: configHome, Policy: isolation.None{}})
		assert.Nil(t, got, "%s: codex must keep the real ~/.codex", name)
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"))
}

// An engine with no in-tree home policy at all (opencode — deferred pending the
// XDG_CONFIG_HOME blast-radius decision) contributes nothing, silently and by
// design.
func TestInTreeAgentHomeEnv_UnregisteredBackendContributesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)

	assert.Nil(t, InTreeAgentHomeEnv(InTreeAgentHome{Backend: "opencode", WorkDir: t.TempDir(), Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}}))
	assert.Nil(t, InTreeAgentHomeEnv(InTreeAgentHome{Backend: "mock", WorkDir: t.TempDir(), Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}}))
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

	got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
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

	got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: mustClaudeInstance(t, workDir, harpA)}, got)
	assert.DirExists(t, mustClaudeInstance(t, workDir, harpA), "the home must exist even when nothing was copied into it")
	assert.Empty(t, strictness.All())
}

// The instance's SHAPE, spelled out once so a change to the layout cannot pass
// by agreeing with itself: state tier (not cache — it holds copied credentials
// nothing rebuilds), keyed by harp, one `home` root, one leaf per engine.
//
// MUTATION TARGET m2: drop the harp from the env contribution (key the instance
// by project again) and this goes red on the missing harp component.
// parked_engines: the kiro/codex rows are commented out with those
// packages — claude alone still proves the shape (state tier, keyed by
// harp, one leaf per engine) until they return.
func TestInTreeAgentHomeEnv_ContributesTheSessionInstanceShape(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	claudeEnv := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	// kiroEnv := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "kiro", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	// codexEnv := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "codex", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})

	instance := filepath.Join(workDir, ".ctxloom", "state", harpA, "home")
	assert.Equal(t, filepath.Join(instance, "claude"), claudeEnv[claude.ConfigDirEnv])
	// assert.Equal(t, filepath.Join(instance, "kiro"), kiroEnv[kiro.HomeEnv])
	// assert.Equal(t, filepath.Join(instance, ".codex"), codexEnv[codex.CodexHomeEnv])

	for _, home := range []string{claudeEnv[claude.ConfigDirEnv]} {
		assert.Contains(t, home, string(filepath.Separator)+harpA+string(filepath.Separator),
			"the instance is keyed by SESSION, not by project")
		assert.NotContains(t, home, filepath.Join(".ctxloom", "cache"))
		assert.NotContains(t, home, filepath.Join("state", "engines"),
			"the retired durable per-project engine home must not regrow")
	}
}

// PER SESSION. Two sessions in ONE checkout get two instances, and what one
// writes the other cannot see — the isolation the in-tree axis did not have
// while the home was per-project.
//
// MUTATION TARGET m1: key the instance by engine instead of by harp and this
// goes red, because session B would find session A's file.
func TestInTreeAgentHomeEnv_TwoSessionsGetTwoInstances(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	a := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpA, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	b := InTreeAgentHomeEnv(InTreeAgentHome{Backend: "claude-code", WorkDir: workDir, Harp: harpB, ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
	require.NotEmpty(t, a)
	require.NotEmpty(t, b)
	assert.NotEqual(t, a[claude.ConfigDirEnv], b[claude.ConfigDirEnv], "two sessions must not share one home")

	// Payload, not just paths: a file session A's agent writes is absent from B.
	require.NoError(t, os.WriteFile(filepath.Join(a[claude.ConfigDirEnv], "session-a-only.json"), []byte(`{"x":1}`), 0o600))
	_, err := os.Stat(filepath.Join(b[claude.ConfigDirEnv], "session-a-only.json"))
	assert.True(t, os.IsNotExist(err), "session B can see session A's engine state")
}

// EMPTY HARP DECLINES. There is no session-less instance and no shared
// fallback — a shared fallback is exactly the durable per-project home the
// model retired. Nothing is contributed and nothing is created.
func TestInTreeAgentHomeEnv_EmptyHarpContributesNothingAndCreatesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	fakeCodexHostHome(t)
	workDir := t.TempDir()

	for _, backend := range []string{"claude-code", "kiro", "codex"} {
		got := InTreeAgentHomeEnv(InTreeAgentHome{Backend: backend, WorkDir: workDir, Harp: "", ConfigHome: agents.ConfigHomeProject, Policy: isolation.None{}})
		assert.Nil(t, got, "%s: a run with no session name gets no instance", backend)
	}
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"),
		"a declined contribution must not leave a directory behind")
}
