package isolation

import (
	"encoding/json"
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
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// Stage 1's session home, over claude's REAL declaration (TestMain installs
// its facts), and the relocators that present it.

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

// fakeHostHome isolates the environment (testsupport.Isolate: a scratch
// $HOME, every var that can authenticate claude or locate its login unset),
// so no case can read or write the developer's real credentials. When token
// is non-empty the host also gets a native ~/.claude login, so a case can
// show that login is never copied.
func fakeHostHome(t *testing.T, token string) string {
	t.Helper()
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	home := testsupport.Isolate(t)
	if token != "" {
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
	return credSpec(t, eng, home, harp, m, engine.Credentials{})
}

// credSpec is homeSpec carrying the run's credentials.
func credSpec(t *testing.T, eng engine.Engine, home, harp string, m agents.HomeMode, c engine.Credentials) Spec {
	t.Helper()
	s, err := NewSpec(launch.Axes{}, eng).Project(t.TempDir()).
		Session(harp, sessionDir(home, harp), SessionState{Harp: harp}).Home(m).Credentials(c).Build()
	require.NoError(t, err)
	return s
}

// claudeCredentials is claude's REAL Credentials for mode, from the
// launching env as the test has set it.
func claudeCredentials(t *testing.T, mode engine.AuthMode) engine.Credentials {
	t.Helper()
	a, ok := claudeEngine(t).Home().Auth.Get()
	require.True(t, ok)
	c, err := a.Credentials(mode, os.LookupEnv)
	require.NoError(t, err)
	return c
}

// placeOn runs stage 1 then r's stage 2 over cwd.
func placeOn(t *testing.T, s Spec, cwd string, r relocator) (launch.Placement, []mount) {
	t.Helper()
	pl, mounts, err := relocateOn(t, s, cwd, r)
	require.NoError(t, err)
	return pl, mounts
}

// relocateOn is placeOn returning stage 2's refusal.
func relocateOn(t *testing.T, s Spec, cwd string, r relocator) (launch.Placement, []mount, error) {
	t.Helper()
	stores, err := stageStores(s.backend(), s.creds.Stores)
	require.NoError(t, err)
	return r.relocate(stageLayout(s, cwd, nil, stores))
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
		"the engine is told the path IT can open, and a run with no credentials gets none")
	assert.Contains(t, mounts, mount{Host: host, Container: target}, "the RIGHT host directory lands at the fixed root")
	assert.DirExists(t, host, "the mount source must exist before the runtime is asked to bind it")
	assert.NoFileExists(t, filepath.Join(host, ".credentials.json"), "no credential is copied into a mapped home")
	assert.Empty(t, strictness.All())
}

// A host run is told the host path itself, shares the human's login in
// place, mounts nothing — and the engine's own instance config is written
// INTO the session home the one rule names (not a leaf beneath it). The
// login's env is exactly what the engine resolved: the storage var set to
// the launching env's string, and the other credentials unset.
func TestSessionHome_HostSeesTheHostPathAndSharesTheLogin(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	creds := claudeCredentials(t, engine.AuthLogin)
	s := credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds)

	pl, mounts := placeOn(t, s, t.TempDir(), hostRelocator{})
	want := claudeHome(home, harpA)
	assert.Nil(t, mounts)
	assert.Equal(t, present.Root{Host: want, Engine: want}, pl.Paths.Paths().SessionHome)
	assert.Equal(t, map[string]string{claude.ConfigDirEnv: want, claude.SecureStorageEnv: ""}, pl.Env)
	assert.Equal(t, creds.Unset, pl.Unset, "the names the engine must not inherit ride the placement")
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

	placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, claudeCredentials(t, engine.AuthLogin)), t.TempDir(), hostRelocator{})

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

// The storage var carries the human's own CLAUDE_CONFIG_DIR byte for byte:
// claude names the macOS keychain item from the exact string.
func TestSessionHome_HostLoginIsTheHumansConfigDirVerbatim(t *testing.T) {
	home := fakeHostHome(t, "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, "custom-claude"), 0o700))
	humans := home + "/./custom-claude/"
	t.Setenv(claude.ConfigDirEnv, humans)

	pl, _ := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, claudeCredentials(t, engine.AuthLogin)), t.TempDir(), hostRelocator{})
	assert.Equal(t, humans, pl.Env[claude.SecureStorageEnv])
	assert.Equal(t, claudeHome(home, harpA), pl.Env[claude.ConfigDirEnv])
}

// A launch from inside a run that already shares the login passes the
// inherited storage on, never that run's own session home.
func TestSessionHome_HostLoginInheritsTheLaunchingRunsStorage(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	t.Setenv(claude.ConfigDirEnv, "/parent/session/home")
	t.Setenv(claude.SecureStorageEnv, "")

	pl, _ := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, claudeCredentials(t, engine.AuthLogin)), t.TempDir(), hostRelocator{})
	assert.Equal(t, "", pl.Env[claude.SecureStorageEnv])
}

// CONTAINER + CLOUD: each provider credential directory the human has is
// mounted READ-ONLY at its place under the container's $HOME; one the human
// does not have is not declared, so nothing is mounted for it.
func TestCredentials_ContainerCloudMountsTheProviderDirsReadOnly(t *testing.T) {
	home := fakeHostHome(t, "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".aws"), 0o700))
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	creds := claudeCredentials(t, engine.AuthCloud)

	pl, mounts := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds), t.TempDir(), containerOf)
	assert.Contains(t, mounts, mount{Host: filepath.Join(home, ".aws"), Container: defaultContainerHome + "/.aws", ReadOnly: true})
	for _, m := range mounts {
		assert.NotContains(t, m.Host, "gcloud", "a provider login the human never made is not mounted")
	}
	assert.Equal(t, "1", pl.Env["CLAUDE_CODE_USE_BEDROCK"], "the provider switch rides the engine's env")
}

// A store that is no directory (claude's login in the macOS Keychain) is
// shared in place on the host and refused by a container, which cannot
// reach it, naming the Keychain and `auth: token`.
func TestCredentials_AKeychainStoreIsHostOnly(t *testing.T) {
	home := fakeHostHome(t, "")
	keychain := engine.Credentials{Stores: []engine.SharedStore{{Var: claude.SecureStorageEnv}}}
	s := credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, keychain)

	pl, _ := placeOn(t, s, t.TempDir(), hostRelocator{})
	assert.Equal(t, "", pl.Env[claude.SecureStorageEnv], "in place on the host")

	_, _, err := relocateOn(t, s, t.TempDir(), containerOf)
	require.ErrorIs(t, err, errStoreNotADirectory)
	require.ErrorIs(t, err, engine.ErrNoCredential)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok)
	assert.Contains(t, fix, "auth: token")
	assert.Contains(t, err.Error(), "Keychain")
}

// A token run's credential rides the engine's env, in a container as on the
// host; it shares no store, so nothing of the human's is mounted.
func TestCredentials_ATokenAuthenticatesAContainerFromItsEnv(t *testing.T) {
	home := fakeHostHome(t, "")
	creds := engine.Credentials{Env: map[string]string{claude.OAuthTokenEnv: tokenFixture}, Unset: []string{claude.SecureStorageEnv}}

	pl, mounts := placeOn(t, credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds), t.TempDir(), containerOf)
	assert.Equal(t, tokenFixture, pl.Env[claude.OAuthTokenEnv])
	assert.Equal(t, []string{claude.SecureStorageEnv}, pl.Unset)
	assert.NotContains(t, pl.Env, claude.SecureStorageEnv, "a token run never points at the human's storage")
	for _, m := range mounts {
		assert.NotEqual(t, filepath.Join(home, ".claude"), m.Host, "nothing of the human's is mounted")
	}
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

	raw, err := os.ReadFile(filepath.Join(claudeHome(home, harpA), claude.InstanceConfigFileName))
	require.NoError(t, err, "the seeded instance config must exist")
	// Decoded, not substring-matched: JSON escapes a Windows path's
	// backslashes, so the raw bytes never contain the path as written.
	var cfg struct {
		Projects map[string]any `json:"projects"`
	}
	require.NoError(t, json.Unmarshal(raw, &cfg))
	assert.Contains(t, cfg.Projects, filepath.Clean(checkout), "the trust entry names the run's cwd")
	assert.NotContains(t, cfg.Projects, filepath.Clean(s.project), "the trust entry does not name the project root the run never enters")
}
