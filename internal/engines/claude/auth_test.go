package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// shellOf is an os.LookupEnv over a map.
func shellOf(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
}

// everyCredentialExported is a launching env that exports every credential
// var claude reads, a provider switch, a profile and the login's storage, so
// anything a mode fails to remove shows up as a leak.
var everyCredentialExported = map[string]string{
	OAuthTokenEnv:             "shell-token",
	APIKeyEnv:                 "shell-key",
	AuthTokenEnv:              "shell-gateway",
	"CLAUDE_CODE_USE_BEDROCK": "1",
	ProfileEnv:                "work",
	SecureStorageEnv:          "/human/.claude",
}

const testEngine = "claude-code"

func testAuth() claudeAuth { return claudeAuth{engine: testEngine} }

// The declared mode decides: only that mode's credential is set, read from
// the launching env, and every variable that would outrank or replace it is
// UNSET, even when the launching env exports all of them.
func TestClaudeAuth_Credentials_TheDeclaredModeDecides(t *testing.T) {
	nonLogin := append([]string{SecureStorageEnv}, providerSwitches...)
	for _, tc := range []struct {
		name      string
		mode      engine.AuthMode
		shell     map[string]string
		wantSet   map[string]string
		wantUnset []string
	}{
		{"token from the launching env", engine.AuthToken, everyCredentialExported,
			map[string]string{OAuthTokenEnv: "shell-token"}, append([]string{APIKeyEnv, AuthTokenEnv}, nonLogin...)},
		{"login shares the storage the launching env resolves", engine.AuthLogin,
			map[string]string{OAuthTokenEnv: "shell-token", ConfigDirEnv: "/h/./cfg/"},
			nil, append(append([]string{OAuthTokenEnv, APIKeyEnv, AuthTokenEnv}, providerSwitches...), ProfileEnv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testAuth().Credentials(tc.mode, shellOf(tc.shell))
			require.NoError(t, err)
			assert.Equal(t, tc.wantSet, got.Env)
			assert.ElementsMatch(t, tc.wantUnset, got.Unset)
			for k := range got.Env {
				assert.NotContains(t, got.Unset, k, "a variable is set or unset, never both")
			}
		})
	}
}

// An empty export is no credential: the mode is refused as if nothing were
// exported, never handed an empty token.
func TestClaudeAuth_Credentials_AnEmptyExportIsNotAValue(t *testing.T) {
	_, err := testAuth().Credentials(engine.AuthToken, shellOf(map[string]string{OAuthTokenEnv: ""}))
	require.ErrorIs(t, err, engine.ErrNoCredential)
}

// The storage var is what the launching env's own claude resolves, byte for
// byte: its own storage var when set (even to ""), else the config dir, else
// "" (HOME/.claude).
func TestClaudeAuth_Credentials_LoginStorageIsWhatTheLaunchingEnvResolves(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"neither set is claude's default", map[string]string{}, ""},
		{"the config dir verbatim, never cleaned", map[string]string{ConfigDirEnv: "/h/./cfg/"}, "/h/./cfg/"},
		{"an inherited storage var wins over the config dir", map[string]string{SecureStorageEnv: "/real", ConfigDirEnv: "/session"}, "/real"},
		{"an inherited empty storage var is kept", map[string]string{SecureStorageEnv: "", ConfigDirEnv: "/session"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testAuth().Credentials(engine.AuthLogin, shellOf(tc.env))
			require.NoError(t, err)
			require.Len(t, got.Stores, 1, "login shares exactly the human's own storage")
			st := got.Stores[0]
			assert.Equal(t, engine.SharedStore{Var: SecureStorageEnv, Value: tc.want, HomeRel: loginStoreHomeRel}, st)
			assert.NotContains(t, got.Env, SecureStorageEnv, "the store's var is the environment's to set, per where the run executes")
		})
	}
}

// The token UNSETS the storage var: "" would mean HOME/.claude, the human's
// own credential, so it must be absent, not empty. And it shares no store of
// the human's: its credential is the launching env's, carried in the env.
func TestClaudeAuth_Credentials_TheTokenTouchesNothingOfTheHumans(t *testing.T) {
	got, err := testAuth().Credentials(engine.AuthToken, shellOf(everyCredentialExported))
	require.NoError(t, err)
	assert.NotContains(t, got.Env, SecureStorageEnv)
	assert.Contains(t, got.Unset, SecureStorageEnv)
	assert.Empty(t, got.Stores)
}

// A missing token is refused, typed, with a remedy naming how the human
// mints and exports one (ctxloom never captures or stores one). Any mode
// outside login and token is not claude's.
func TestClaudeAuth_Credentials_RefusalsAreTypedWithARemedy(t *testing.T) {
	_, err := testAuth().Credentials(engine.AuthToken, shellOf(nil))
	require.ErrorIs(t, err, engine.ErrNoCredential)
	var r report.Remediable
	require.ErrorAs(t, err, &r)
	for _, w := range []string{"claude setup-token", "export " + OAuthTokenEnv, "secret manager"} {
		assert.Contains(t, r.Remedy(), w)
	}
	assert.NotContains(t, r.Remedy(), "ctxloom auth", "ctxloom stores no credential, so no ctxloom command supplies one")
	_, err = testAuth().Credentials("api-key", shellOf(everyCredentialExported))
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}

// claude's token is created by its own `setup-token` flow and carried in
// OAuthTokenEnv: init runs exactly those arguments on the human's terminal
// and names exactly that variable in the export line it prints.
func TestClaudeAuth_DeclaresItsTokenSetup(t *testing.T) {
	var a engine.Auth = testAuth()
	setup, ok := a.(engine.TokenSetup)
	require.True(t, ok, "claude's token is minted by its own CLI, so its Auth declares how")
	assert.Equal(t, []string{"setup-token"}, setup.SetupArgs())
	assert.Equal(t, OAuthTokenEnv, setup.TokenEnv())
}
