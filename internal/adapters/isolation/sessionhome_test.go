package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Stage 1's session home, over claude's REAL declaration (TestMain installs
// its facts) and the relocators that present it. These are the properties
// operations.ResolveInTreeAgentHome held before the home moved here.

// The two session names every case here keys its homes by.
const (
	harpA = "ugly-icy-squid"
	harpB = "brave-warm-otter"
)

// tokenFixture stands in for the token `claude setup-token` mints.
const tokenFixture = "sk-ant-oat01-fixture"

// hostLoginFixture is the user's own native ~/.claude login, which no case
// may find copied anywhere.
const hostLoginFixture = `{"claudeAiOauth":{"accessToken":"native-access","refreshToken":"native-refresh"}}`

// claudeEngine is claude's real kind.
func claudeEngine(t *testing.T) engine.Engine {
	t.Helper()
	k, err := claude.Build()
	require.NoError(t, err)
	return k
}

// fakeHostHome points $HOME at a scratch directory and clears every var that
// can authenticate claude. When token is non-empty it is exported as the
// setup-token AND the host gets a native ~/.claude login, so a case can show
// that login is never copied. No case can read or write the developer's
// real credentials.
func fakeHostHome(t *testing.T, token string) string {
	t.Helper()
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, ok := TokenAuthFor(claude.EngineName)
	require.True(t, ok)
	for _, v := range append([]string{a.TokenVar}, a.EnvTriggers...) {
		t.Setenv(v, "")
	}
	// UNSET, not empty: the shared login reads whether the launching env
	// sets these at all (engine.SharedLogin.Value).
	for _, v := range []string{claude.ConfigDirEnv, claude.SecureStorageEnv} {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
	if token != "" {
		t.Setenv(a.TokenVar, token)
		writeNativeLogin(t, home)
	}
	return home
}

func writeNativeLogin(t *testing.T, home string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(hostLoginFixture), 0o600))
}

// sessionDir is where Resolve places harp's session dir under home.
func sessionDir(home, harp string) string {
	return filepath.Join(home, ".ctxloom", "sessions", harp)
}

// claudeHome is the one rule's claude home for harp (launch.SessionHome).
func claudeHome(home, harp string) string {
	return filepath.Join(sessionDir(home, harp), "home", claude.HomeLeaf)
}

// homeSpec is a host spec for eng in harp's session, under home.
func homeSpec(t *testing.T, eng engine.Engine, home, harp string, m agents.HomeMode) Spec {
	t.Helper()
	s, err := NewSpec(launch.Axes{}, eng).Project(t.TempDir()).
		Session(harp, sessionDir(home, harp), SessionState{Harp: harp}).Home(m).Build()
	require.NoError(t, err)
	return s
}

// hostLogin is the shared login a HOST run is handed beside its home: the
// human's own credential storage as the launching env resolves it, and the
// setup-token blanked, because claude reads a token ahead of any credential.
func hostLogin(storage string) map[string]string {
	return map[string]string{claude.SecureStorageEnv: storage, claude.OAuthTokenEnv: ""}
}

// placeOn runs stage 1 then r's stage 2 over cwd.
func placeOn(t *testing.T, s Spec, cwd string, r relocator) (launch.Placement, []mount) {
	t.Helper()
	pl, mounts, err := r.relocate(stageLayout(s, cwd, nil, r.sharesLogin()))
	require.NoError(t, err)
	return pl, mounts
}

var containerOf = containerRelocator{rt: fakeRuntime{name: "docker", available: true}, instanceHome: defaultContainerInstanceHome, home: defaultContainerHome}

// A container gets the SAME session home the host gets: the bytes stay on
// the host, the engine is told the in-container path, and one mount makes
// that true.
func TestSessionHome_ContainerGetsTheSessionHomeMapped(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

	pl, mounts := placeOn(t, s, t.TempDir(), containerOf)
	host := claudeHome(home, harpA)
	target := defaultContainerInstanceHome + "/" + claude.HomeLeaf
	assert.Equal(t, present.Root{Host: host, Engine: target}, pl.Paths.Paths().SessionHome)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: target}, pl.Env,
		"the engine is told the path IT can open, and a container shares no login")
	assert.Contains(t, mounts, mount{Host: host, Container: target}, "the RIGHT host directory lands at the fixed root")
	assert.DirExists(t, host, "the mount source must exist before the runtime is asked to bind it")
	assert.NoFileExists(t, filepath.Join(host, ".credentials.json"), "no credential is copied into a mapped home")
	assert.Empty(t, strictness.All())
}

// A host run is told the host path itself, shares the human's login, mounts
// nothing — and the engine's own instance config is written INTO the session
// home the one rule names (not a leaf beneath it).
func TestSessionHome_HostSeesTheHostPathAndSharesTheLogin(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)

	pl, mounts := placeOn(t, s, t.TempDir(), hostRelocator{})
	want := claudeHome(home, harpA)
	assert.Nil(t, mounts)
	assert.Equal(t, present.Root{Host: want, Engine: want}, pl.Paths.Paths().SessionHome)
	env := map[string]string{claude.ConfigDirEnv: want}
	for k, v := range hostLogin("") {
		env[k] = v
	}
	assert.Equal(t, env, pl.Env)
	assert.FileExists(t, filepath.Join(want, claude.InstanceConfigFileName), "the engine's instance config is written in the session home itself")
	assert.NoFileExists(t, filepath.Join(want, claude.HomeLeaf, claude.InstanceConfigFileName), "not one leaf further down")
	assert.NoFileExists(t, filepath.Join(want, ".credentials.json"), "the native login is never copied into a session home")
	assert.Empty(t, strictness.All())
}

// The human's own ~/.claude is READ and never written.
func TestSessionHome_NeverWritesTheRealHostHome(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	before, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)

	placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), hostRelocator{})

	after, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "the host credential's bytes changed")
	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	require.NoError(t, err)
	assert.Len(t, entries, 1, "preparing the session home added files to the human's own ~/.claude")
}

// THE SCOPING RULE. Only a binding that EXPLICITLY selects host keeps the
// real home: no session home, no env, nothing created. The undeclared
// binding and the zero value both get the session home.
func TestSessionHome_OnlyTheHostSelectionKeepsTheRuntimeHome(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeHost)

	pl, _ := placeOn(t, s, t.TempDir(), hostRelocator{})
	assert.Empty(t, pl.Env, "declared host: handed no config-home override")
	assert.Empty(t, pl.Paths.Paths().SessionHome, "declared host: no session home")
	assert.NoDirExists(t, sessionDir(home, harpA), "a declined run must not even create the session dir")

	undeclared, err := agents.ParseHomeMode("")
	require.NoError(t, err)
	for name, m := range map[string]agents.HomeMode{"undeclared": undeclared, "zero value": ""} {
		pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, m), t.TempDir(), hostRelocator{})
		assert.Equal(t, claudeHome(home, harpA), pl.Env[claude.ConfigDirEnv], "%s: gets the session home", name)
	}
}

// THE SCRATCH RULING: an engine that relocates nothing still gets a session
// home — created, presented at the host path on the host, and as $HOME in a
// container — with no home var to set.
func TestSessionHome_NonRelocatingEngineGetsOneToo(t *testing.T) {
	home := fakeHostHome(t, "")
	s := homeSpec(t, mock.New(), home, harpA, agents.HomeModeSession)
	want := filepath.Join(sessionDir(home, harpA), "home", string(mock.Name))

	pl, _ := placeOn(t, s, t.TempDir(), hostRelocator{})
	assert.Equal(t, present.Root{Host: want, Engine: want}, pl.Paths.Paths().SessionHome)
	assert.Empty(t, pl.Env)
	assert.DirExists(t, want, "the session home is created (C6)")

	pl, mounts := placeOn(t, s, t.TempDir(), containerOf)
	assert.Equal(t, present.Root{Host: want, Engine: defaultContainerHome}, pl.Paths.Paths().SessionHome)
	assert.Contains(t, mounts, mount{Host: want, Container: defaultContainerHome})
	assert.Empty(t, strictness.All())
}

// A HOST run shares the human's own login in place, so it needs no token.
func TestSessionHome_HostRunNeedsNoToken(t *testing.T) {
	home := fakeHostHome(t, "")
	writeNativeLogin(t, home)

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), hostRelocator{})
	assert.Equal(t, claudeHome(home, harpA), pl.Env[claude.ConfigDirEnv])
	assert.Equal(t, "", pl.Env[claude.SecureStorageEnv])
	assert.NoFileExists(t, filepath.Join(claudeHome(home, harpA), ".credentials.json"), "the login is shared in place, never copied")
	assert.Empty(t, strictness.All(), "a host run with the human's login is authenticated")
}

// The storage var carries the human's own CLAUDE_CONFIG_DIR byte for byte:
// claude names the macOS keychain item from the exact string.
func TestSessionHome_HostLoginIsTheHumansConfigDirVerbatim(t *testing.T) {
	home := fakeHostHome(t, "")
	const humans = "/home/someone/./custom-claude/"
	t.Setenv(claude.ConfigDirEnv, humans)

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), hostRelocator{})
	assert.Equal(t, humans, pl.Env[claude.SecureStorageEnv])
	assert.Equal(t, claudeHome(home, harpA), pl.Env[claude.ConfigDirEnv])
}

// A launch from inside a run that already shares the login passes the
// inherited storage on, never that run's own session home.
func TestSessionHome_HostLoginInheritsTheLaunchingRunsStorage(t *testing.T) {
	home := fakeHostHome(t, "")
	t.Setenv(claude.ConfigDirEnv, "/parent/session/home")
	t.Setenv(claude.SecureStorageEnv, "")

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), hostRelocator{})
	assert.Equal(t, "", pl.Env[claude.SecureStorageEnv])
}

// Nothing to authenticate a CONTAINER run with is FAIL-LOUD: it cannot reach
// the human's login, so with no token and no API var, a session home would
// strand the agent logged out. The finding names the remedies, and the run
// has no session home.
func TestSessionHome_ContainerWithNoTokenFailsLoud(t *testing.T) {
	home := fakeHostHome(t, "")
	writeNativeLogin(t, home) // a native login does not count: it is never copied

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), containerOf)
	assert.Empty(t, pl.Paths.Paths().SessionHome, "no home the engine could not authenticate in")
	assert.Empty(t, pl.Env)

	found := strictness.All()
	require.Len(t, found, 1, "an unauthenticatable session home must fail loud")
	assert.Equal(t, report.KindIsolation, found[0].Kind)
	for _, want := range []string{"claude setup-token", "ctxloom auth set-token", "ANTHROPIC_API_KEY", "engine_home: host"} {
		assert.Contains(t, found[0].Text, want)
	}
	assert.Contains(t, found[0].Remedy, "ctxloom auth set-token", "a finding without a fix-it leaves the user stuck")
}

// The API-key path authenticates a container from its env.
func TestSessionHome_ApiKeyAuthenticatesAContainerHome(t *testing.T) {
	home := fakeHostHome(t, "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	pl, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), t.TempDir(), containerOf)
	assert.NotEmpty(t, pl.Env[claude.ConfigDirEnv])
	assert.DirExists(t, claudeHome(home, harpA))
	assert.Empty(t, strictness.All())
}

// PER SESSION. Two sessions in one checkout get two homes, and what one
// writes the other cannot see.
func TestSessionHome_TwoSessionsGetTwoHomes(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	cwd := t.TempDir()

	a, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession), cwd, hostRelocator{})
	b, _ := placeOn(t, homeSpec(t, claudeEngine(t), home, harpB, agents.HomeModeSession), cwd, hostRelocator{})
	ha, hb := a.Env[claude.ConfigDirEnv], b.Env[claude.ConfigDirEnv]
	require.NotEmpty(t, ha)
	assert.NotEqual(t, ha, hb, "two sessions must not share one home")
	require.NoError(t, os.WriteFile(filepath.Join(ha, "session-a-only.json"), []byte(`{"x":1}`), 0o600))
	assert.NoFileExists(t, filepath.Join(hb, "session-a-only.json"), "session B can see session A's engine state")
}

// No session, no home: a Spec without one does not build, so nothing is
// created and there is no session-less fallback.
func TestSessionHome_NoSessionDoesNotBuild(t *testing.T) {
	_, err := NewSpec(launch.Axes{}, claudeEngine(t)).Project(t.TempDir()).Build()
	require.ErrorIs(t, err, ErrSpecIncomplete)
	assert.Contains(t, err.Error(), "session")
}

// The workspace-trust answer names the directory the engine RUNS in (a
// worktree's checkout), not the project root it never enters.
func TestSessionHome_TrustNamesTheRunCwd(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	s := homeSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession)
	checkout := t.TempDir()

	placeOn(t, s, checkout, hostRelocator{})

	cfg, err := os.ReadFile(filepath.Join(claudeHome(home, harpA), claude.InstanceConfigFileName))
	require.NoError(t, err, "the seeded instance config must exist")
	assert.Contains(t, string(cfg), checkout, "the trust entry names the run's cwd")
	assert.NotContains(t, string(cfg), s.project+`"`, "the trust entry does not name the project root the run never enters")
}
