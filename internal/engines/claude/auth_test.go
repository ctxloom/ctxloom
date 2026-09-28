package claude

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// memStore is a CredentialReader over a map: absent modes are ErrNoCredential.
type memStore map[engine.AuthMode]string

func (m memStore) Read(mode engine.AuthMode) ([]byte, error) {
	v, ok := m[mode]
	if !ok {
		return nil, fmt.Errorf("fake store: %w", engine.ErrNoCredential)
	}
	return []byte(v), nil
}

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

// The declared mode decides: only that mode's credential is set, and every
// variable that would outrank or replace it is UNSET, even when the
// launching env exports all of them. A shell value wins for the declared
// mode only.
func TestClaudeAuth_LaunchEnv_TheDeclaredModeDecides(t *testing.T) {
	stored := memStore{engine.AuthToken: "stored-token\n", engine.AuthAPIKey: "stored-key"}
	nonLogin := append([]string{SecureStorageEnv}, providerSwitches...)
	for _, tc := range []struct {
		name      string
		mode      engine.AuthMode
		shell     map[string]string
		wantSet   map[string]string
		wantUnset []string
	}{
		{"token from the store, trimmed", engine.AuthToken, map[string]string{APIKeyEnv: "shell-key"},
			map[string]string{OAuthTokenEnv: "stored-token"}, append([]string{APIKeyEnv, AuthTokenEnv}, nonLogin...)},
		{"token exported wins over the stored one", engine.AuthToken, everyCredentialExported,
			map[string]string{OAuthTokenEnv: "shell-token"}, append([]string{APIKeyEnv, AuthTokenEnv}, nonLogin...)},
		{"api-key from the store", engine.AuthAPIKey, map[string]string{OAuthTokenEnv: "shell-token"},
			map[string]string{APIKeyEnv: "stored-key"}, append([]string{OAuthTokenEnv, AuthTokenEnv}, nonLogin...)},
		{"api-key exported wins over the stored one", engine.AuthAPIKey, everyCredentialExported,
			map[string]string{APIKeyEnv: "shell-key"}, append([]string{OAuthTokenEnv, AuthTokenEnv}, nonLogin...)},
		{"login shares the storage the launching env resolves", engine.AuthLogin,
			map[string]string{OAuthTokenEnv: "shell-token", ConfigDirEnv: "/h/./cfg/"},
			map[string]string{SecureStorageEnv: "/h/./cfg/"}, append(append([]string{OAuthTokenEnv, APIKeyEnv, AuthTokenEnv}, providerSwitches...), ProfileEnv)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := testAuth().LaunchEnv(tc.mode, shellOf(tc.shell), stored)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSet, got.Set)
			assert.ElementsMatch(t, tc.wantUnset, got.Unset)
			for k := range got.Set {
				assert.NotContains(t, got.Unset, k, "a variable is set or unset, never both")
			}
		})
	}
}

// An empty export is no credential: it neither wins nor hides the store.
func TestClaudeAuth_LaunchEnv_AnEmptyExportIsNotAValue(t *testing.T) {
	got, err := testAuth().LaunchEnv(engine.AuthToken, shellOf(map[string]string{OAuthTokenEnv: ""}), memStore{engine.AuthToken: "stored"})
	require.NoError(t, err)
	assert.Equal(t, "stored", got.Set[OAuthTokenEnv])
}

// The storage var is what the launching env's own claude resolves, byte for
// byte: its own storage var when set (even to ""), else the config dir, else
// "" (HOME/.claude).
func TestClaudeAuth_LaunchEnv_LoginStorageIsWhatTheLaunchingEnvResolves(t *testing.T) {
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
			got, err := testAuth().LaunchEnv(engine.AuthLogin, shellOf(tc.env), memStore{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Set[SecureStorageEnv])
		})
	}
}

// Every mode but login UNSETS the storage var: "" would mean HOME/.claude,
// the human's own credential, so it must be absent, not empty.
func TestClaudeAuth_LaunchEnv_NonLoginUnsetsTheStorageVar(t *testing.T) {
	shell := shellOf(map[string]string{"CLAUDE_CODE_USE_VERTEX": "1", SecureStorageEnv: "/human/.claude"})
	for _, mode := range []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey, engine.AuthCloud} {
		got, err := testAuth().LaunchEnv(mode, shell, memStore{engine.AuthToken: "t", engine.AuthAPIKey: "k"})
		require.NoError(t, err)
		assert.NotContains(t, got.Set, SecureStorageEnv, mode)
		assert.Contains(t, got.Unset, SecureStorageEnv, mode)
	}
}

// cloud passes the provider's own configuration through from the shell —
// only what is set — and removes the stored modes' credentials and the
// login's storage.
func TestClaudeAuth_LaunchEnv_CloudPassesTheProviderThrough(t *testing.T) {
	bedrock := map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-east-1", "AWS_PROFILE": "work", "UNRELATED": "x"}
	for k, v := range everyCredentialExported {
		if _, ok := bedrock[k]; !ok && k != AuthTokenEnv {
			bedrock[k] = v
		}
	}
	got, err := testAuth().LaunchEnv(engine.AuthCloud, shellOf(bedrock), memStore{engine.AuthToken: "t"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "us-east-1", "AWS_PROFILE": "work"}, got.Set)
	assert.ElementsMatch(t, []string{OAuthTokenEnv, APIKeyEnv, SecureStorageEnv, ProfileEnv}, got.Unset)

	gateway := map[string]string{AuthTokenEnv: "bearer", "ANTHROPIC_BASE_URL": "https://gw.example"}
	got, err = testAuth().LaunchEnv(engine.AuthCloud, shellOf(gateway), memStore{})
	require.NoError(t, err)
	assert.Equal(t, gateway, got.Set, "a gateway's bearer and base URL select cloud on their own")
}

// Every refusal is typed and carries a remedy naming what to do: the
// command that stores or mints the credential, the variables cloud needs,
// and — for cloud — the other modes THIS engine supports, derived from
// Modes().
func TestClaudeAuth_LaunchEnv_RefusalsAreTypedWithARemedy(t *testing.T) {
	for _, tc := range []struct {
		mode engine.AuthMode
		want []string
	}{
		{engine.AuthToken, []string{"ctxloom auth mint --engine claude-code --mode token", OAuthTokenEnv}},
		{engine.AuthAPIKey, []string{"ctxloom auth set --engine claude-code --mode api-key", APIKeyEnv}},
		{engine.AuthCloud, []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", AuthTokenEnv}},
	} {
		_, err := testAuth().LaunchEnv(tc.mode, shellOf(nil), memStore{})
		require.ErrorIs(t, err, engine.ErrNoCredential, tc.mode)
		var r report.Remediable
		require.ErrorAs(t, err, &r, tc.mode)
		for _, w := range tc.want {
			assert.Contains(t, r.Remedy(), w, tc.mode)
		}
		if tc.mode == engine.AuthCloud {
			for _, m := range testAuth().Modes() {
				if m != engine.AuthCloud {
					assert.Contains(t, r.Remedy(), string(m), "the alternatives come from Modes()")
				}
			}
		}
	}
	_, err := testAuth().LaunchEnv("keychain", shellOf(nil), memStore{})
	require.ErrorIs(t, err, engine.ErrAuthModeUnsupported)
}

// fakeClaude writes an executable `claude` whose setup-token prints script's
// output, and returns its path.
func fakeClaude(t *testing.T, output string, exit int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = setup-token ] || exit 9\nread _ignored\nprintf '%%s' '%s'\nexit %d\n", output, exit)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

// Mint runs `claude setup-token` on the terminal it is handed, lets its
// output reach that terminal, and returns just the token.
func TestClaudeAuth_Mint_ReturnsTheTokenSetupTokenPrinted(t *testing.T) {
	bin := fakeClaude(t, "Your OAuth token (valid for 1 year):\n\nsk-ant-oat01-FAKE_fake-123\n\nStore it securely.\n", 0)
	var out bytes.Buffer
	tok, err := claudeAuth{binary: bin}.Mint(context.Background(), engine.AuthToken, engine.Terminal{In: bytes.NewBufferString("\n"), Out: &out})
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-oat01-FAKE_fake-123", string(tok))
	assert.Contains(t, out.String(), "Store it securely", "the flow's own output still reaches the human")
	assert.NotContains(t, out.String(), "sk-ant-oat01-FAKE", "the minted token is stored, never echoed")
	assert.Contains(t, out.String(), redactedToken)
}

func TestClaudeAuth_Mint_RefusesWhenNoTokenWasPrinted(t *testing.T) {
	bin := fakeClaude(t, "Login cancelled.\n", 0)
	_, err := claudeAuth{binary: bin}.Mint(context.Background(), engine.AuthToken, engine.Terminal{In: bytes.NewBufferString("\n")})
	require.ErrorIs(t, err, errNoTokenPrinted)
}

func TestClaudeAuth_Mint_SurfacesAFailedFlow(t *testing.T) {
	bin := fakeClaude(t, "sk-ant-oat01-NOTUSED", 3)
	_, err := claudeAuth{binary: bin}.Mint(context.Background(), engine.AuthToken, engine.Terminal{In: bytes.NewBufferString("\n")})
	require.ErrorContains(t, err, "claude setup-token")
}

// Only the token is mintable: a key is the human's to supply, and the login
// is theirs already.
func TestClaudeAuth_Mint_OnlyTheToken(t *testing.T) {
	for _, mode := range []engine.AuthMode{engine.AuthAPIKey, engine.AuthLogin, engine.AuthCloud} {
		_, err := claudeAuth{binary: "/nonexistent"}.Mint(context.Background(), mode, engine.Terminal{})
		require.ErrorIs(t, err, engine.ErrMintUnsupported, mode)
	}
}

// The redactor catches a token split across writes, shows a prompt with no
// newline at once, and holds back only a fragment that could still become a
// token.
func TestTokenRedactor(t *testing.T) {
	var out bytes.Buffer
	r := &tokenRedactor{w: &out}
	_, _ = r.Write([]byte("Press Enter to open the browser: "))
	assert.Equal(t, "Press Enter to open the browser: ", out.String(), "a prompt without a newline is shown immediately")
	_, _ = r.Write([]byte("token sk-ant-"))
	_, _ = r.Write([]byte("oat01-AB"))
	_, _ = r.Write([]byte("CD_ef\nnext"))
	r.flush()
	assert.Equal(t, "Press Enter to open the browser: token "+redactedToken+"\nnext", out.String())
	assert.NotContains(t, out.String(), "ABCD")
}
