package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
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
	if creds != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(creds), 0o600))
	}
	return home
}

// mustClaudeInstance resolves one session's instance through the owning
// engine package's OWN helper, so these assertions cannot drift from the
// resolution the production path uses.
func mustClaudeInstance(t *testing.T, workDir, harp string) string {
	t.Helper()
	dir, err := claude.SessionConfigDir(workDir, harp)
	require.NoError(t, err)
	return dir
}

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

// containerInstanceRoot stands in for the fixed in-container instance root
// (isolation.ContainerInstanceHome). The engine's home lands under it at the
// leaf the ENGINE declares — an in-container path that is NOT the host path,
// so the Engine side of the root is observably distinct from the Host side.
const containerInstanceRoot = "/ctxloom-test/home"

// projectHome is the input every case starts from: an agent binding that
// declared config_home: project, on the host (no runtime advice).
func projectHome(workDir, harp string) InTreeAgentHome {
	return InTreeAgentHome{
		Backend:    "claude-code",
		WorkDir:    workDir,
		Cwd:        workDir,
		Harp:       harp,
		ConfigHome: agents.ConfigHomeProject,
	}
}

// requireResolutionInvariant pins the one shape a resolution can never take:
// an empty root with no stated reason. Every case runs its result through
// this, so a code path that declines without saying why cannot be added
// without going red here.
func requireResolutionInvariant(t *testing.T, res AgentHomeResolution) {
	t.Helper()
	if res.Root.Host == "" {
		require.NotEmpty(t, res.Absent, "a resolution with no home must say why")
		assert.Empty(t, res.Env, "an absent home contributes no env")
		assert.Nil(t, res.Mount, "an absent home mounts nothing")
		return
	}
	require.Empty(t, res.Absent, "a present home carries no absence reason")
	require.NotEmpty(t, res.Root.Engine, "a present home names an engine-side path")
	if res.Root.Engine == res.Root.Host {
		assert.Nil(t, res.Mount, "Engine == Host: nothing to mount")
	} else {
		require.NotNil(t, res.Mount, "Engine != Host: a mount must make the engine-side path true")
	}
}

// THE DEFECT: a container cell used to get no relocated home at all — the
// resolver declined on the policy, and the container's fresh in-container
// $HOME was ephemeral, unmapped and uninspectable. Now the SAME session home
// the host cell gets is handed to the container: the bytes stay on the host,
// the engine is told the in-container path, and one mount makes that true.
func TestResolveInTreeAgentHome_ContainerGetsTheSessionHomeMapped(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	in := projectHome(workDir, harpA)
	in.ContainerHome = containerInstanceRoot
	res := ResolveInTreeAgentHome(in)
	requireResolutionInvariant(t, res)

	// The leaf is the ENGINE's declared HomeVar.Subdir (claude.HomeLeaf), taken
	// from the declaration — never re-derived from the host path.
	target := containerInstanceRoot + "/" + claude.HomeLeaf
	host := mustClaudeInstance(t, workDir, harpA)
	assert.Equal(t, present.Root{Host: host, Engine: target}, res.Root)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: target}, res.Env,
		"the engine is told the path IT can open, never the host path")
	require.NotNil(t, res.Mount)
	assert.Equal(t, present.Mount{HostDir: host, TargetDir: target}, *res.Mount,
		"the RIGHT host directory — this session's instance leaf — lands at the fixed root")
	assert.DirExists(t, host, "the mount source must exist before the runtime is asked to bind it")
	assert.FileExists(t, filepath.Join(host, ".credentials.json"), "the mapped home is seeded exactly like the host cell's")
	assert.Empty(t, strictness.All())
}

// A host cell (no container instance root) is told the host path itself and
// mounts nothing.
func TestResolveInTreeAgentHome_HostCellEngineSeesTheHostPath(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()
	want := mustClaudeInstance(t, workDir, harpA)

	res := ResolveInTreeAgentHome(projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)
	assert.Equal(t, present.Root{Host: want, Engine: want}, res.Root)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want}, res.Env)
	assert.Nil(t, res.Mount)
}

// t1 — an in-tree AGENT run for claude-code is handed CLAUDE_CONFIG_DIR at the
// project-scoped state home, and the host credential is really there,
// owner-only, WHOLE. The refresh token survives, and that is the point: a
// credential stripped of it works until the access token expires and then that
// instance is stuck, which is the defect the provisioner replaced. Material now
// reaches the home by a delivery the engine DECLARED it accepts — mounted or
// replicated — and both can renew.
//
// The env var alone would be a half-truth: a controlled home claude cannot
// authenticate against is worse than no relocation at all. A home whose
// credential cannot RENEW is the same half-truth on a timer.
func TestResolveInTreeAgentHome_ClaudeGetsASeededControlledHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	res := ResolveInTreeAgentHome(projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)

	want := mustClaudeInstance(t, workDir, harpA)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want}, res.Env)

	seeded, err := os.ReadFile(filepath.Join(want, ".credentials.json"))
	require.NoError(t, err, "the controlled home must actually carry the seeded credential")
	require.NotEmpty(t, seeded, "empty-source guard: the fixture must carry bytes")
	assert.Contains(t, string(seeded), "seed-fixture-token", "the access token is seeded so the home authenticates")
	assert.Contains(t, string(seeded), "seed-fixture-refresh",
		"the refresh token SURVIVES: a delivered credential must be able to renew, and stripping it was the defect the copy path carried")
	assert.Contains(t, string(seeded), "refreshToken",
		"the refresh-token field is present; nothing projects it away any more")

	info, err := os.Stat(filepath.Join(want, ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a seeded credential is owner-only")

	assert.Empty(t, strictness.All(), "a fully seeded home records no finding")
}

// t1b — the host's own ~/.claude is READ and never written. There is no
// migration on this axis (nothing has ever lived at the new path); the human's
// home is somebody else's property.
func TestResolveInTreeAgentHome_NeverWritesTheRealHostHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	home := fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	before, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)

	ResolveInTreeAgentHome(projectHome(workDir, harpA))

	after, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the host credential's bytes changed")

	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "seeding added files to the human's own ~/.claude")
}

// THE SCOPING RULE, declining half. ONLY an EXPLICIT `config_home: host`
// keeps the REAL host home, and it says so. This is now the sole input that
// shares the human's engine home; the cases that used to sit beside it here
// (no binding at all, and an undeclared binding) moved to the granting half
// below. MUTATION TARGET m2 — ignore a declared host value and this goes red.
func TestResolveInTreeAgentHome_ExplicitHostKeepsTheRuntimeHomeAndSaysSo(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	in := projectHome(workDir, harpA)
	in.ConfigHome = agents.ConfigHomeHost
	res := ResolveInTreeAgentHome(in)
	requireResolutionInvariant(t, res)
	assert.Empty(t, res.Env, "an explicit host declaration must be handed no config-home override")
	assert.Contains(t, res.Absent, "config_home", "the reason names the policy that declined")
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"),
		"a declined run must not even create the instance root")
}

// THE SCOPING RULE, granting half — and the case the whole flip exists for.
// A run with NO agent binding at all (ConfigHome == "", a plain `ctxloom
// run`) carries the zero value and never reaches agents.ParseConfigHome; it
// is gated in ResolveInTreeAgentHome alone. It must get a controlled home,
// as must an AGENT-BOUND run whose binding never declares config_home.
// Neither of these says anything about config_home, and that silence is not
// consent to share the human's ~/.claude.
//
// MUTATION TARGET m1 — restore the gate to `!= ConfigHomeProject` and the
// "no binding" case goes red alone; flip agents.ParseConfigHome's "" arm
// back to host and "undeclared" goes red alone.
func TestResolveInTreeAgentHome_UndeclaredAndBindinglessBothGetAPrivateHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)

	undeclared, err := agents.ParseConfigHome("")
	require.NoError(t, err)

	cases := map[string]agents.ConfigHome{
		"no binding": "",
		"undeclared": undeclared,
	}
	for name, ch := range cases {
		t.Run(name, func(t *testing.T) {
			workDir := t.TempDir()
			in := projectHome(workDir, harpA)
			in.ConfigHome = ch
			res := ResolveInTreeAgentHome(in)
			requireResolutionInvariant(t, res)
			assert.Empty(t, res.Absent, "%s: must get a controlled home, not a reason it has none", name)
			assert.NotEmpty(t, res.Env, "%s: the engine must be pointed at the controlled home", name)
			assert.DirExists(t, filepath.Join(workDir, ".ctxloom", "state"),
				"%s: the per-session instance root must exist", name)
			for _, v := range res.Env {
				assert.Contains(t, v, filepath.Join(".ctxloom", "state", harpA),
					"%s: the home var must name THIS session's instance, not the real host home", name)
			}
		})
	}
}

// An engine that declares no relocatable home cannot be given one, on any
// cell. That is no longer silent: the resolution carries the engine's own
// stated reason, because a binding that asked for `config_home: project` and
// got nothing deserves to learn why.
func TestResolveInTreeAgentHome_EngineWithoutAHomeSaysWhy(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)

	in := projectHome(t.TempDir(), harpA)
	in.Backend = "mock"
	res := ResolveInTreeAgentHome(in)
	requireResolutionInvariant(t, res)
	assert.Contains(t, res.Absent, "mock", "the reason names the engine")
}

// Nothing to seed is FAIL-LOUD, never a silent relocation: with neither
// ANTHROPIC_API_KEY nor a host credential file, pointing claude at an empty
// controlled home would strand the agent logged out. Record the ClassIsolation
// finding the choke owner aborts on, and resolve ABSENT with the reason — so a
// --degraded run falls back to the home its runtime gives it, instead of
// launching against a home that cannot authenticate.
func TestResolveInTreeAgentHome_NothingToSeedFailsLoudAndIsAbsent(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, "") // no ~/.claude at all, no API key
	workDir := t.TempDir()

	res := ResolveInTreeAgentHome(projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)
	assert.Contains(t, res.Absent, "ANTHROPIC_API_KEY")

	found := strictness.All()
	require.Len(t, found, 1, "an unauthenticatable controlled home must fail loud")
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
	assert.Contains(t, found[0].Message, "ANTHROPIC_API_KEY")
	assert.NotEmpty(t, found[0].FixIt, "a finding without a fix-it leaves the user stuck")
}

// The API-key path: auth rides the environment, so there is nothing to seed and
// nothing to fail about — the controlled home is still handed over, and it
// exists.
func TestResolveInTreeAgentHome_ApiKeyAuthenticatesAFreshControlledHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	workDir := t.TempDir()

	res := ResolveInTreeAgentHome(projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: mustClaudeInstance(t, workDir, harpA)}, res.Env)
	assert.DirExists(t, mustClaudeInstance(t, workDir, harpA), "the home must exist even when nothing was copied into it")
	assert.Empty(t, strictness.All())
}

// The instance's SHAPE, spelled out once so a change to the layout cannot pass
// by agreeing with itself: state tier (not cache — it holds copied credentials
// nothing rebuilds), keyed by harp, one `home` root, one leaf per engine.
//
// MUTATION TARGET m2: drop the harp from the env contribution (key the instance
// by project again) and this goes red on the missing harp component.
func TestResolveInTreeAgentHome_ContributesTheSessionInstanceShape(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	res := ResolveInTreeAgentHome(projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)

	instance := filepath.Join(workDir, ".ctxloom", "state", harpA, "home")
	home := res.Env[claude.ConfigDirEnv]
	assert.Equal(t, filepath.Join(instance, "claude"), home)
	assert.Contains(t, home, string(filepath.Separator)+harpA+string(filepath.Separator),
		"the instance is keyed by SESSION, not by project")
	assert.NotContains(t, home, filepath.Join(".ctxloom", "cache"))
	assert.NotContains(t, home, filepath.Join("state", "engines"),
		"the retired durable per-project engine home must not regrow")
}

// PER SESSION. Two sessions in ONE checkout get two instances, and what one
// writes the other cannot see — the isolation the in-tree axis did not have
// while the home was per-project.
//
// MUTATION TARGET m1: key the instance by engine instead of by harp and this
// goes red, because session B would find session A's file.
func TestResolveInTreeAgentHome_TwoSessionsGetTwoInstances(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	a := ResolveInTreeAgentHome(projectHome(workDir, harpA))
	b := ResolveInTreeAgentHome(projectHome(workDir, harpB))
	require.NotEmpty(t, a.Env)
	require.NotEmpty(t, b.Env)
	assert.NotEqual(t, a.Env[claude.ConfigDirEnv], b.Env[claude.ConfigDirEnv], "two sessions must not share one home")

	// Payload, not just paths: a file session A's agent writes is absent from B.
	require.NoError(t, os.WriteFile(filepath.Join(a.Env[claude.ConfigDirEnv], "session-a-only.json"), []byte(`{"x":1}`), 0o600))
	_, err := os.Stat(filepath.Join(b.Env[claude.ConfigDirEnv], "session-a-only.json"))
	assert.True(t, os.IsNotExist(err), "session B can see session A's engine state")
}

// EMPTY HARP DECLINES, and says so. There is no session-less instance and no
// shared fallback — a shared fallback is exactly the durable per-project home
// the model retired. Nothing is contributed and nothing is created.
func TestResolveInTreeAgentHome_EmptyHarpIsAbsentAndCreatesNothing(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()

	res := ResolveInTreeAgentHome(projectHome(workDir, ""))
	requireResolutionInvariant(t, res)
	assert.Contains(t, res.Absent, "session", "the reason names the missing session")
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"),
		"a declined contribution must not leave a directory behind")
}

// The workspace-trust answer the seed generates names the directory the engine
// actually RUNS in (Cwd), which is not the project root on a worktree cell:
// trusting the project root would answer for a directory the run never enters.
func TestResolveInTreeAgentHome_TrustNamesTheRunCwdNotTheProjectRoot(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, hostCredentialFixture)
	workDir := t.TempDir()
	checkout := t.TempDir()

	in := projectHome(workDir, harpA)
	in.Cwd = checkout
	res := ResolveInTreeAgentHome(in)
	requireResolutionInvariant(t, res)

	cfg, err := os.ReadFile(filepath.Join(res.Root.Host, ".claude.json"))
	require.NoError(t, err, "the seeded instance config must exist")
	assert.Contains(t, string(cfg), checkout, "the trust entry names the run's cwd")
	assert.NotContains(t, string(cfg), workDir+`"`, "the trust entry does not name the project root the run never enters")
}
