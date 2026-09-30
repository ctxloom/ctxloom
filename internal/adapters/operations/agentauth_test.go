package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/enginefixture"
)

// fakeEnvAuth is an engine auth that reads each mode's credential from its
// own var in the launching env, refusing with its own remedy when it is not
// exported.
type fakeEnvAuth struct{}

const (
	fakeTokenVar = "FAKE_TOKEN"
	fakeKeyVar   = "FAKE_KEY"
)

var fakeModeVar = map[engine.AuthMode]string{engine.AuthToken: fakeTokenVar, engine.AuthAPIKey: fakeKeyVar}

func (fakeEnvAuth) Modes() []engine.AuthMode {
	return []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey}
}

func (fakeEnvAuth) Credentials(mode engine.AuthMode, shell func(string) (string, bool)) (engine.Credentials, error) {
	v := fakeModeVar[mode]
	secret, ok := shell(v)
	if !ok || secret == "" {
		return engine.Credentials{}, report.Errorf("export "+v, "fake %s: %w", mode, engine.ErrNoCredential)
	}
	return engine.Credentials{Env: map[string]string{v: secret}}, nil
}

// installFakeAuth stands a fake-auth engine in front of the registry, under
// a scratch HOME, with none of its vars exported.
func installFakeAuth(t *testing.T) engine.Registry {
	t.Helper()
	testsupport.Isolate(t)
	for _, v := range fakeModeVar {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
	return enginefixture.Install(t, enginefixture.Kind("fake-auth", mock.WithHome(engine.HomeSpec{
		Vars: []engine.HomeVar{{Name: "FAKE_HOME", Subdir: ".fake"}},
		Auth: engine.Provide[engine.Auth](fakeEnvAuth{}),
	})))
}

// A run's credential is the one the launching env exports for the agent's
// mode, handed over as the engine's Credentials say.
func TestResolveRunAuth_ReadsTheCredentialFromTheLaunchingEnv(t *testing.T) {
	reg := installFakeAuth(t)
	t.Setenv(fakeTokenVar, "env-secret")
	env, err := resolveRunAuth(reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthToken)})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{fakeTokenVar: "env-secret"}, env.Env)
}

// A credential the launching env does not export is the ENGINE's refusal,
// typed and with the engine's own remedy — never a ctxloom command, since
// ctxloom stores no credential — at a terminal or not: nothing prompts.
func TestResolveRunAuth_AMissingCredentialIsTheEnginesRefusal(t *testing.T) {
	reg := installFakeAuth(t)
	for _, mode := range []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey} {
		_, err := resolveRunAuth(reg, runAuth{Backend: "fake-auth", Declared: string(mode)})
		require.ErrorIs(t, err, engine.ErrNoCredential, mode)
		assert.Equal(t, "export "+fakeModeVar[mode], remedyOf(t, err), mode)
	}
}

func TestResolveRunAuth_RefusesAModeTheEngineLacks(t *testing.T) {
	reg := installFakeAuth(t)
	_, err := resolveRunAuth(reg, runAuth{Backend: "fake-auth", Declared: string(engine.AuthLogin)})
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}

// Nothing to resolve: an engine that declares no auth.
func TestResolveRunAuth_NothingToResolve(t *testing.T) {
	reg := enginefixture.Install(t, enginefixture.Kind("no-auth"))
	env, err := resolveRunAuth(reg, runAuth{Backend: "no-auth"})
	require.NoError(t, err)
	assert.Zero(t, env)
}

// A claude agent declaring login shares the human's own credential storage,
// as the launching env resolves it, verbatim, and every other credential —
// including a token the human exported — is unset. Nothing about where the
// run executes enters: the store is data the environment satisfies.
func TestResolveRunAuth_LoginSharesTheHumansStorage(t *testing.T) {
	fakeHostHome(t, tokenFixture)
	t.Setenv(claude.OAuthTokenEnv, tokenFixture)
	t.Setenv(claude.ConfigDirEnv, "/home/me/./.claude-work/")
	creds, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthLogin)})
	require.NoError(t, err)
	assert.Empty(t, creds.Env)
	require.Len(t, creds.Stores, 1)
	assert.Equal(t, claude.SecureStorageEnv, creds.Stores[0].Var)
	assert.Equal(t, "/home/me/./.claude-work/", creds.Stores[0].Value)
	assert.False(t, creds.Stores[0].ReadOnly, "the run refreshes the shared login")
	assert.Subset(t, creds.Unset, []string{claude.OAuthTokenEnv, claude.APIKeyEnv, claude.AuthTokenEnv})
}

// A claude token agent gets the token the launching env exports, with an
// exported API key unset: the declared mode decides.
func TestResolveRunAuth_ClaudeTokenFromTheLaunchingEnv(t *testing.T) {
	fakeHostHome(t, "")
	t.Setenv(claude.APIKeyEnv, "sk-ant-api-shell")
	t.Setenv(claude.OAuthTokenEnv, tokenFixture)
	env, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthToken)})
	require.NoError(t, err)
	assert.Equal(t, tokenFixture, env.Env[claude.OAuthTokenEnv])
	assert.Empty(t, env.Stores, "a token shares nothing of the human's")
	assert.Contains(t, env.Unset, claude.APIKeyEnv)
	assert.Contains(t, env.Unset, claude.SecureStorageEnv)
}

// A claude token agent with no token exported is refused with claude's
// remedy: the human mints it with `claude setup-token` and exports it.
func TestResolveRunAuth_ClaudeTokenMissingNamesSetupToken(t *testing.T) {
	fakeHostHome(t, "")
	t.Setenv("PATH", t.TempDir())
	_, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthToken)})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.Contains(t, remedyOf(t, err), "claude setup-token")
	assert.Contains(t, remedyOf(t, err), "export "+claude.OAuthTokenEnv)
}

// cloud with nothing selected is the engine's own refusal.
func TestResolveRunAuth_CloudWithNothingSelectedIsRefused(t *testing.T) {
	fakeHostHome(t, "")
	_, err := resolveRunAuth(engines.Registry(), runAuth{Backend: claude.EngineName, Declared: string(engine.AuthCloud)})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	requireRemedyNamingModes(t, err, engine.AuthCloud)
}

// requireRemedyNamingModes asserts err carries a remedy that names every
// mode claude supports except the one refused, read from its Modes().
func requireRemedyNamingModes(t *testing.T, err error, refused engine.AuthMode) {
	t.Helper()
	var r report.Remediable
	require.ErrorAs(t, err, &r)
	kind, ok := engines.Registry().Lookup(claude.EngineName)
	require.True(t, ok)
	a, ok := kind.Home().Auth.Get()
	require.True(t, ok)
	for _, m := range a.Modes() {
		if m != refused {
			assert.Contains(t, r.Remedy(), string(m))
		}
	}
}

// ONE check, two doors: every invalid selection is refused by the same
// typed error at write time (agent create/edit) and at run time (a launch).
func TestAuthSelection_WriteAndRunRefuseAlike(t *testing.T) {
	fakeHostHome(t, "")
	for _, tc := range []struct {
		name     string
		llm      string
		mode     string
		sentinel error
	}{
		{"an unknown mode", "claude-code", "apikey", engine.ErrUnknownAuthMode},
		{"any mode on an engine with no auth", "mock", "token", engine.ErrEngineHasNoAuth},
		{"cloud with nothing selected", "claude-code", "cloud", engine.ErrNoCredential},
		{"api-key with no key", "claude-code", "api-key", engine.ErrNoCredential},
		{"token with no token", "claude-code", "token", engine.ErrNoCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, appDir := loadConfigDir(t, fmt.Sprintf("version: %d\n", config.CurrentConfigVersion))
			_, werr := SetAgent(context.Background(), managerFor(t, appDir), cfg, SetAgentRequest{
				Name: "a", LLM: ptr(tc.llm), Profiles: ptr([]string{"x"}), Auth: ptr(tc.mode),
			})
			require.ErrorIs(t, werr, tc.sentinel, "write")
			_, rerr := resolveRunAuth(engines.Registry(), runAuth{Backend: tc.llm, Declared: tc.mode})
			require.ErrorIs(t, rerr, tc.sentinel, "run")
			for _, err := range []error{werr, rerr} {
				var r report.Remediable
				require.ErrorAs(t, err, &r)
				assert.NotEmpty(t, r.Remedy())
			}
		})
	}
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
