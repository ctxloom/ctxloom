package claude

import (
	"os"
	"path/filepath"
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
		{"api-key from the launching env", engine.AuthAPIKey, everyCredentialExported,
			map[string]string{APIKeyEnv: "shell-key"}, append([]string{OAuthTokenEnv, AuthTokenEnv}, nonLogin...)},
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
	for mode, v := range map[engine.AuthMode]string{engine.AuthToken: OAuthTokenEnv, engine.AuthAPIKey: APIKeyEnv} {
		_, err := testAuth().Credentials(mode, shellOf(map[string]string{v: ""}))
		require.ErrorIs(t, err, engine.ErrNoCredential, mode)
	}
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
			assert.Equal(t, engine.SharedStore{Var: SecureStorageEnv, Value: tc.want, HomeRel: loginStoreHomeRel}, got.Stores[0],
				"read-write: the human's claude and the run share the credential and its refresh locks")
			assert.NotContains(t, got.Env, SecureStorageEnv, "the store's var is the environment's to set, per where the run executes")
		})
	}
}

// Every mode but login UNSETS the storage var: "" would mean HOME/.claude,
// the human's own credential, so it must be absent, not empty.
func TestClaudeAuth_Credentials_NonLoginUnsetsTheStorageVar(t *testing.T) {
	shell := shellOf(map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", SecureStorageEnv: "/human/.claude", OAuthTokenEnv: "t", APIKeyEnv: "k"})
	for _, mode := range []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey, engine.AuthCloud} {
		got, err := testAuth().Credentials(mode, shell)
		require.NoError(t, err)
		assert.NotContains(t, got.Env, SecureStorageEnv, mode)
		assert.Contains(t, got.Unset, SecureStorageEnv, mode)
	}
}

// cloud passes the provider's own configuration through from the shell —
// only what is set — and removes the token and api-key credentials and the
// login's storage.
func TestClaudeAuth_Credentials_CloudPassesTheProviderThrough(t *testing.T) {
	bedrock := map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-east-1", "AWS_PROFILE": "work", "UNRELATED": "x"}
	for k, v := range everyCredentialExported {
		if _, ok := bedrock[k]; !ok && k != AuthTokenEnv {
			bedrock[k] = v
		}
	}
	got, err := testAuth().Credentials(engine.AuthCloud, shellOf(bedrock))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-east-1", "AWS_PROFILE": "work"}, got.Env)
	assert.ElementsMatch(t, []string{OAuthTokenEnv, APIKeyEnv, SecureStorageEnv, ProfileEnv}, got.Unset)

	gateway := map[string]string{AuthTokenEnv: "bearer", "ANTHROPIC_BASE_URL": "https://gw.example"}
	got, err = testAuth().Credentials(engine.AuthCloud, shellOf(gateway))
	require.NoError(t, err)
	assert.Equal(t, gateway, got.Env, "a gateway's bearer and base URL select cloud on their own")
	assert.Empty(t, got.Stores, "a launching env with no home shares nothing")
}

// cloud shares each provider credential directory the human HAS, read-only
// and at its place under $HOME — and declares none that is missing, since a
// declared store that is missing refuses the run.
func TestClaudeAuth_Credentials_CloudSharesTheProviderDirsThatExist(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "gcloud"), 0o700))
	vertex := shellOf(map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", "HOME": home})

	got, err := testAuth().Credentials(engine.AuthCloud, vertex)
	require.NoError(t, err)
	assert.Equal(t, []engine.SharedStore{{HomeRel: ".config/gcloud", ReadOnly: true}}, got.Stores, "only gcloud exists")

	require.NoError(t, os.MkdirAll(filepath.Join(home, ".aws"), 0o700))
	got, err = testAuth().Credentials(engine.AuthCloud, vertex)
	require.NoError(t, err)
	assert.Equal(t, []engine.SharedStore{{HomeRel: ".aws", ReadOnly: true}, {HomeRel: ".config/gcloud", ReadOnly: true}}, got.Stores)
	for _, st := range got.Stores {
		assert.True(t, st.ReadOnly, "a run uses the human's provider login, never changes it")
		assert.Empty(t, st.Var, "the SDK finds it under $HOME with no variable")
	}
}

// AWS SSO refreshes its access token by rewriting the cache under
// ~/.aws/sso/cache, so a read-only ~/.aws would break an SSO login at its
// first refresh. cloud therefore shares that one directory read-write, NESTED
// in the read-only ~/.aws and declared after it (a mount placed before its
// parent would be shadowed by it) — and only when it exists, like every
// provider store.
func TestClaudeAuth_Credentials_CloudSharesTheSSOCacheReadWrite(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".aws", "sso", "cache"), 0o700))
	bedrock := shellOf(map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "HOME": home})

	got, err := testAuth().Credentials(engine.AuthCloud, bedrock)
	require.NoError(t, err)
	assert.Equal(t, []engine.SharedStore{{HomeRel: ".aws", ReadOnly: true}, {HomeRel: ".aws/sso/cache"}}, got.Stores,
		"the SSO cache is shared read-write, after the read-only ~/.aws it is nested in")
}

// The provider stores belong to cloud alone: a mode that does not read the
// provider's files maps none of them, even when the human has every one.
func TestClaudeAuth_Credentials_OnlyCloudSharesProviderStores(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{filepath.Join(".aws", "sso", "cache"), filepath.Join(".config", "gcloud"), ".claude"} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, dir), 0o700))
	}
	for _, mode := range []engine.AuthMode{engine.AuthLogin, engine.AuthToken, engine.AuthAPIKey} {
		got, err := testAuth().Credentials(mode, shellOf(map[string]string{"HOME": home, OAuthTokenEnv: "t", APIKeyEnv: "k"}))
		require.NoError(t, err, mode)
		for _, st := range got.Stores {
			assert.NotContains(t, providerStores, st, "%s must not share the provider store %q", mode, st.HomeRel)
		}
	}
}

// cloud declares which of the vars it passes through name a credential FILE
// — only those the launching env sets — so an environment that runs claude
// elsewhere can present the file; the value stays the human's path. No other
// mode reads a provider file, so none declares one even when every file var
// is exported.
func TestClaudeAuth_Credentials_OnlyCloudDeclaresCredentialFiles(t *testing.T) {
	shell := shellOf(map[string]string{
		"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_CONFIG_FILE": "/h/aws-config", "GOOGLE_APPLICATION_CREDENTIALS": "/h/adc.json",
		OAuthTokenEnv: "t", APIKeyEnv: "k",
	})
	got, err := testAuth().Credentials(engine.AuthCloud, shell)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"AWS_CONFIG_FILE", "GOOGLE_APPLICATION_CREDENTIALS"}, got.FileVars)
	assert.Equal(t, "/h/aws-config", got.Env["AWS_CONFIG_FILE"], "declared, not rewritten: where it is presented is the environment's call")

	for _, mode := range []engine.AuthMode{engine.AuthLogin, engine.AuthToken, engine.AuthAPIKey} {
		got, err := testAuth().Credentials(mode, shell)
		require.NoError(t, err, mode)
		assert.Empty(t, got.FileVars, mode)
		assert.NotContains(t, got.Env, "AWS_CONFIG_FILE", mode)
	}
}

// Token and api-key share no store of the human's: their credential is the
// launching env's, carried in the env.
func TestClaudeAuth_Credentials_EnvModesShareNoStore(t *testing.T) {
	for _, mode := range []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey} {
		got, err := testAuth().Credentials(mode, shellOf(everyCredentialExported))
		require.NoError(t, err)
		assert.Empty(t, got.Stores, mode)
	}
}

// Every refusal is typed and carries a remedy naming what to do: the
// variable to export (the token minted by the human with claude's own
// `setup-token` — ctxloom never captures or stores one), the variables cloud
// needs, and — for cloud — the other modes THIS engine supports, derived
// from Modes().
func TestClaudeAuth_Credentials_RefusalsAreTypedWithARemedy(t *testing.T) {
	for _, tc := range []struct {
		mode engine.AuthMode
		want []string
	}{
		{engine.AuthToken, []string{"claude setup-token", "export " + OAuthTokenEnv, "secret manager"}},
		{engine.AuthAPIKey, []string{"export " + APIKeyEnv}},
		{engine.AuthCloud, []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", AuthTokenEnv}},
	} {
		_, err := testAuth().Credentials(tc.mode, shellOf(nil))
		require.ErrorIs(t, err, engine.ErrNoCredential, tc.mode)
		var r report.Remediable
		require.ErrorAs(t, err, &r, tc.mode)
		for _, w := range tc.want {
			assert.Contains(t, r.Remedy(), w, tc.mode)
		}
		assert.NotContains(t, r.Remedy(), "ctxloom auth", "ctxloom stores no credential, so no ctxloom command supplies one")
		if tc.mode == engine.AuthCloud {
			for _, m := range testAuth().Modes() {
				if m != engine.AuthCloud {
					assert.Contains(t, r.Remedy(), string(m), "the alternatives come from Modes()")
				}
			}
		}
	}
	_, err := testAuth().Credentials("keychain", shellOf(nil))
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}
