package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// fakeEnvAuth is an engine auth offering only the token, read from its own
// var in the launching env, refusing with its own remedy when it is not
// exported.
type fakeEnvAuth struct{}

const fakeTokenVar = "FAKE_TOKEN"

func (fakeEnvAuth) Modes() []engine.AuthMode { return []engine.AuthMode{engine.AuthToken} }

func (fakeEnvAuth) Credentials(mode engine.AuthMode, shell func(string) (string, bool)) (engine.Credentials, error) {
	secret, ok := shell(fakeTokenVar)
	if !ok || secret == "" {
		return engine.Credentials{}, report.Errorf("export "+fakeTokenVar, "fake %s: %w", mode, engine.ErrNoCredential)
	}
	return engine.Credentials{Env: map[string]string{fakeTokenVar: secret}}, nil
}

// installFakeAuth stands a fake-auth engine in front of the registry, under
// a scratch HOME, with its token not exported.
func installFakeAuth(t *testing.T) engine.Registry {
	t.Helper()
	testsupport.Isolate(t)
	t.Setenv(fakeTokenVar, "")
	require.NoError(t, os.Unsetenv(fakeTokenVar))
	return enginefixture.Install(t, enginefixture.Kind("fake-auth", mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FAKE_HOME", Subdir: ".fake"}},
		Auth: engine.Provide[engine.Auth](fakeEnvAuth{}),
	})))
}

// A run's credential is the one the launching env exports for its mode,
// handed over as the engine's Credentials say, stamped with that mode.
func TestResolveRunAuth_ReadsTheCredentialFromTheLaunchingEnv(t *testing.T) {
	reg := installFakeAuth(t)
	t.Setenv(fakeTokenVar, "env-secret")
	creds, err := resolveRunAuth(reg, runAuth{Backend: "fake-auth", Mode: engine.AuthToken})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{fakeTokenVar: "env-secret"}, creds.Env)
	assert.Equal(t, engine.AuthToken, creds.Mode)
}

// A credential the launching env does not export is the ENGINE's refusal,
// typed and with the engine's own remedy — never a ctxloom command, since
// ctxloom stores no credential — at a terminal or not: nothing prompts.
func TestResolveRunAuth_AMissingCredentialIsTheEnginesRefusal(t *testing.T) {
	reg := installFakeAuth(t)
	_, err := resolveRunAuth(reg, runAuth{Backend: "fake-auth", Mode: engine.AuthToken})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Equal(t, "export "+fakeTokenVar, remedyOf(t, err))
}

// The human's login on an engine that offers none is refused, typed, naming
// the token (which every engine with auth offers).
func TestResolveRunAuth_RefusesALoginTheEngineLacks(t *testing.T) {
	reg := installFakeAuth(t)
	_, err := resolveRunAuth(reg, runAuth{Backend: "fake-auth", Mode: engine.AuthLogin})
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
	assert.Contains(t, remedyOf(t, err), "auth: token")
}

// Nothing to resolve: an engine that declares no auth, whatever the mode.
func TestResolveRunAuth_NothingToResolve(t *testing.T) {
	reg := enginefixture.Install(t, enginefixture.Kind("no-auth"))
	creds, err := resolveRunAuth(reg, runAuth{Backend: "no-auth", Mode: engine.AuthToken})
	require.NoError(t, err)
	assert.Zero(t, creds)
}

// The human's login on claude shares their own credential storage, as the
// launching env resolves it, verbatim, and every other credential —
// including a token the human exported — is unset. Nothing about where the
// run executes enters: the store is data the environment satisfies.
func TestResolveRunAuth_LoginSharesTheHumansStorage(t *testing.T) {
	fakeHostHome(t, tokenFixture)
	t.Setenv(claude.OAuthTokenEnv, tokenFixture)
	t.Setenv(claude.ConfigDirEnv, "/home/me/./.claude-work/")
	creds, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthLogin})
	require.NoError(t, err)
	assert.Empty(t, creds.Env)
	assert.Equal(t, engine.AuthLogin, creds.Mode)
	require.Len(t, creds.Stores, 1)
	assert.Equal(t, claude.SecureStorageEnv, creds.Stores[0].Var)
	assert.Equal(t, "/home/me/./.claude-work/", creds.Stores[0].Value)
	assert.Subset(t, creds.Unset, []string{claude.OAuthTokenEnv, claude.APIKeyEnv, claude.AuthTokenEnv})
}

// A claude token run gets the token the launching env exports, with an
// exported API key unset: the mode decides.
func TestResolveRunAuth_ClaudeTokenFromTheLaunchingEnv(t *testing.T) {
	fakeHostHome(t, "")
	t.Setenv(claude.APIKeyEnv, "sk-ant-api-shell")
	t.Setenv(claude.OAuthTokenEnv, tokenFixture)
	creds, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthToken})
	require.NoError(t, err)
	assert.Equal(t, tokenFixture, creds.Env[claude.OAuthTokenEnv])
	assert.Empty(t, creds.Stores, "a token shares nothing of the human's")
	assert.Contains(t, creds.Unset, claude.APIKeyEnv)
	assert.Contains(t, creds.Unset, claude.SecureStorageEnv)
}

// A claude token run with no token exported is refused with claude's
// remedy: the human mints it with `claude setup-token` and exports it.
func TestResolveRunAuth_ClaudeTokenMissingNamesSetupToken(t *testing.T) {
	fakeHostHome(t, "")
	t.Setenv("PATH", t.TempDir())
	_, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Mode: engine.AuthToken})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Contains(t, remedyOf(t, err), "claude setup-token")
	assert.Contains(t, remedyOf(t, err), "export "+claude.OAuthTokenEnv)
}

// remedyOf is the remedy err carries, failing when it carries none.
func remedyOf(t *testing.T, err error) string {
	t.Helper()
	var r report.Remediable
	require.ErrorAs(t, err, &r)
	return r.Remedy()
}

// fakeHostHome isolates the environment (testsupport.Isolate: a scratch
// $HOME, the ambient ctxloom session cleared), clears every credential var
// claude reads and UNSETS the ones its login store is resolved from, so no
// case reads the developer's real credentials or a live session's
// CLAUDE_CONFIG_DIR. When token is non-empty the host also gets a native
// ~/.claude login.
func fakeHostHome(t *testing.T, token string) string {
	t.Helper()
	home := testsupport.Isolate(t)
	for _, v := range []string{claude.OAuthTokenEnv, claude.APIKeyEnv, claude.AuthTokenEnv} {
		t.Setenv(v, "")
	}
	for _, v := range []string{claude.ConfigDirEnv, claude.SecureStorageEnv} {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
	if token != "" {
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"native-access"}}`), 0o600))
	}
	return home
}
