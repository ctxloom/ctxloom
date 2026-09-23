package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetEngineHomeStrictness gives each case its own findings state: several of
// these assert on whether a ClassIsolation finding was recorded, which a
// leftover from a sibling case would silently satisfy.
func resetEngineHomeStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
}

// fakeHostHome points $HOME at a scratch directory and clears every var that
// can authenticate claude. When token is non-empty it is exported as the
// setup-token AND the host gets a native ~/.claude login, so a case can show
// that login is never copied. Returns the home path. Every case that touches
// claude uses this so no test can read (or write) the developer's real
// credentials.
func fakeHostHome(t *testing.T, token string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, ok := isolation.TokenAuthFor("claude-code")
	require.True(t, ok)
	for _, v := range append([]string{a.TokenVar}, a.EnvTriggers...) {
		t.Setenv(v, "")
	}
	if token != "" {
		t.Setenv(a.TokenVar, token)
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(hostLoginFixture), 0o600))
	}
	return home
}

// mustClaudeInstance resolves one session's instance the way the descriptor
// does — the session home plus the engine's declared HomeVar.Subdir — so
// these assertions cannot drift from the resolution the production path uses.
func mustClaudeInstance(t *testing.T, workDir, harp string) string {
	t.Helper()
	root, err := paths.HarpSessionEngineHomes(harp)
	require.NoError(t, err)
	return filepath.Join(root, claude.HomeLeaf)
}

// The two session names every case here keys its instances by.
const (
	harpA = "ugly-icy-squid"
	harpB = "brave-warm-otter"
)

// tokenFixture stands in for the token `claude setup-token` mints.
const tokenFixture = "sk-ant-oat01-fixture"

// hostLoginFixture is the user's own native ~/.claude login, which no case
// may find copied anywhere.
const hostLoginFixture = `{"claudeAiOauth":{"accessToken":"native-access","refreshToken":"native-refresh"}}`

// containerInstanceRoot stands in for the fixed in-container instance root
// (isolation.ContainerInstanceHome). The engine's home lands under it at the
// leaf the ENGINE declares — an in-container path that is NOT the host path,
// so the Engine side of the root is observably distinct from the Host side.
const containerInstanceRoot = "/ctxloom-test/home"

// projectHome is the input every case starts from: an agent binding that
// declared engine_home: session, on the host (no runtime advice).
func resolveHome(t *testing.T, in InTreeAgentHome) AgentHomeResolution {
	t.Helper()
	return ResolveInTreeAgentHome(in)
}

func projectHome(workDir, harp string) InTreeAgentHome {
	return InTreeAgentHome{
		Backend:  "claude-code",
		Cwd:      workDir,
		Harp:     harp,
		HomeMode: agents.HomeModeSession,
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
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	in := projectHome(workDir, harpA)
	in.ContainerHome = containerInstanceRoot
	res := resolveHome(t, in)
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
	assert.NoFileExists(t, filepath.Join(host, ".credentials.json"), "no credential is copied into a mapped home either")
	assert.Empty(t, strictness.All())
}

// A host cell (no container instance root) is told the host path itself and
// mounts nothing.
func TestResolveInTreeAgentHome_HostCellEngineSeesTheHostPath(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()
	want := mustClaudeInstance(t, workDir, harpA)

	res := resolveHome(t, projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)
	assert.Equal(t, present.Root{Host: want, Engine: want}, res.Root)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want}, res.Env)
	assert.Nil(t, res.Mount)
}

// The session home authenticates from the token in the env alone: the
// engine var points at the session instance, and the user's native login is
// NOT copied there. A child and its root are prepared identically, so a
// claude child whose owner runs another engine needs nothing from the
// owner's session home.
func TestResolveInTreeAgentHome_ClaudeHomeCopiesNoCredential(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	res := resolveHome(t, projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)

	want := mustClaudeInstance(t, workDir, harpA)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want}, res.Env)
	assert.NoFileExists(t, filepath.Join(want, ".credentials.json"), "the native login is never copied into a session home")
	assert.FileExists(t, filepath.Join(want, ".claude.json"), "the engine's own instance config is still written")
	assert.Empty(t, strictness.All())
}

// t1b — the host's own ~/.claude is READ and never written. There is no
// migration on this axis (nothing has ever lived at the new path); the human's
// home is somebody else's property.
func TestResolveInTreeAgentHome_NeverWritesTheRealHostHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	home := fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	before, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)

	resolveHome(t, projectHome(workDir, harpA))

	after, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the host credential's bytes changed")

	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "preparing the session home added files to the human's own ~/.claude")
}

// THE SCOPING RULE. Only a binding that EXPLICITLY selects host keeps the
// REAL host home — and says so. An undeclared binding (agents.ParseHomeMode's
// default) and the zero value nobody parsed both get the session home: the
// zero value must never silently mean the real home. MUTATION TARGET m1
// flips agents.ParseHomeMode's default to host (the undeclared case goes
// red); m2 ignores a declared host value (the declared case goes red).
func TestResolveInTreeAgentHome_OnlyTheHostSelectionKeepsTheRuntimeHomeAndSaysSo(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	in := projectHome(workDir, harpA)
	in.HomeMode = agents.HomeModeHost
	res := resolveHome(t, in)
	requireResolutionInvariant(t, res)
	assert.Empty(t, res.Env, "declared host: must be handed no config-home override")
	assert.Contains(t, res.Absent, "engine_home", "declared host: the reason names the policy that declined")
	assert.NoDirExists(t, filepath.Join(workDir, ".ctxloom", "state"),
		"a declined run must not even create the instance root")

	undeclared, err := agents.ParseHomeMode("")
	require.NoError(t, err)
	for name, ch := range map[string]agents.HomeMode{"undeclared": undeclared, "zero value": ""} {
		in := projectHome(workDir, harpA)
		in.HomeMode = ch
		res := resolveHome(t, in)
		requireResolutionInvariant(t, res)
		assert.NotEmpty(t, res.Env, "%s: gets the session home", name)
		assert.Empty(t, res.Absent, name)
	}
}

// An engine that declares no relocatable home cannot be given one, on any
// cell. That is no longer silent: the resolution carries the engine's own
// stated reason, because a binding that asked for `engine_home: session` and
// got nothing deserves to learn why.
func TestResolveInTreeAgentHome_EngineWithoutAHomeSaysWhy(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, tokenFixture)

	in := projectHome(t.TempDir(), harpA)
	in.Backend = "mock"
	res := resolveHome(t, in)
	requireResolutionInvariant(t, res)
	assert.Contains(t, res.Absent, "mock", "the reason names the engine")
}

// Nothing to authenticate with is FAIL-LOUD, never a silent relocation: with
// no token and no API var, pointing claude at the controlled home would
// strand the agent logged out. Record the ClassIsolation
// finding the choke owner aborts on, and resolve ABSENT with the reason — so a
// --degraded run falls back to the home its runtime gives it, instead of
// launching against a home that cannot authenticate.
func TestResolveInTreeAgentHome_NoTokenFailsLoudAndIsAbsent(t *testing.T) {
	resetEngineHomeStrictness(t)
	home := fakeHostHome(t, "") // no token, no API key
	// A native login on the host does not count: it is never copied.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(hostLoginFixture), 0o600))
	workDir := t.TempDir()

	res := resolveHome(t, projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)
	assert.Contains(t, res.Absent, "ctxloom auth set-token")

	found := strictness.All()
	require.Len(t, found, 1, "an unauthenticatable controlled home must fail loud")
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
	for _, want := range []string{"claude setup-token", "ctxloom auth set-token", "ANTHROPIC_API_KEY", "engine_home: host"} {
		assert.Contains(t, found[0].Message, want)
	}
	assert.Contains(t, found[0].FixIt, "ctxloom auth set-token", "a finding without a fix-it leaves the user stuck")
}

// The API-key path: auth rides the environment, so there is nothing to fail
// about — the controlled home is still handed over, and it exists.
func TestResolveInTreeAgentHome_ApiKeyAuthenticatesAFreshControlledHome(t *testing.T) {
	resetEngineHomeStrictness(t)
	fakeHostHome(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	workDir := t.TempDir()

	res := resolveHome(t, projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: mustClaudeInstance(t, workDir, harpA)}, res.Env)
	assert.DirExists(t, mustClaudeInstance(t, workDir, harpA), "the home must exist")
	assert.Empty(t, strictness.All())
}

// The instance's SHAPE, spelled out once so a change to the layout cannot pass
// by agreeing with itself: the session's own directory under the ctxloom home
// (not the project tree, not cache — it holds engine state nothing
// rebuilds), keyed by harp, one `home` root, one leaf per engine.
//
// MUTATION TARGET m2: drop the harp from the env contribution (key the instance
// by project again) and this goes red on the missing harp component.
func TestResolveInTreeAgentHome_ContributesTheSessionInstanceShape(t *testing.T) {
	resetEngineHomeStrictness(t)
	hostHome := fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	res := resolveHome(t, projectHome(workDir, harpA))
	requireResolutionInvariant(t, res)

	instance := filepath.Join(hostHome, ".ctxloom", "sessions", harpA, "home")
	home := res.Env[claude.ConfigDirEnv]
	assert.Equal(t, filepath.Join(instance, "claude"), home)
	assert.Contains(t, home, string(filepath.Separator)+harpA+string(filepath.Separator),
		"the instance is keyed by SESSION, not by project")
	assert.NotContains(t, home, workDir, "the project tree holds no session state")
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
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	a := resolveHome(t, projectHome(workDir, harpA))
	b := resolveHome(t, projectHome(workDir, harpB))
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
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()

	res := resolveHome(t, projectHome(workDir, ""))
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
	fakeHostHome(t, tokenFixture)
	workDir := t.TempDir()
	checkout := t.TempDir()

	in := projectHome(workDir, harpA)
	in.Cwd = checkout
	res := resolveHome(t, in)
	requireResolutionInvariant(t, res)

	cfg, err := os.ReadFile(filepath.Join(res.Root.Host, ".claude.json"))
	require.NoError(t, err, "the seeded instance config must exist")
	assert.Contains(t, string(cfg), checkout, "the trust entry names the run's cwd")
	assert.NotContains(t, string(cfg), workDir+`"`, "the trust entry does not name the project root the run never enters")
}
