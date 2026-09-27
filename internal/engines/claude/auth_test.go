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
// var claude reads, so a blanking that is skipped shows up as a leak.
var everyCredentialExported = map[string]string{
	OAuthTokenEnv: "shell-token",
	APIKeyEnv:     "shell-key",
	AuthTokenEnv:  "shell-gateway",
}

// The declared mode decides: only that mode's credential reaches claude, and
// every other credential var is blanked, even when the launching env exports
// all of them. A shell value wins for the declared mode only.
func TestClaudeAuth_LaunchEnv_TheDeclaredModeDecides(t *testing.T) {
	stored := memStore{engine.AuthToken: "stored-token\n", engine.AuthAPIKey: "stored-key"}
	for _, tc := range []struct {
		name  string
		mode  engine.AuthMode
		shell map[string]string
		want  map[string]string
	}{
		{"token from the store, trimmed, others blanked", engine.AuthToken, map[string]string{APIKeyEnv: "shell-key", AuthTokenEnv: "shell-gateway"},
			map[string]string{OAuthTokenEnv: "stored-token", APIKeyEnv: "", AuthTokenEnv: ""}},
		{"token exported wins over the stored one", engine.AuthToken, everyCredentialExported,
			map[string]string{OAuthTokenEnv: "shell-token", APIKeyEnv: "", AuthTokenEnv: ""}},
		{"api-key from the store, token blanked", engine.AuthAPIKey, map[string]string{OAuthTokenEnv: "shell-token"},
			map[string]string{OAuthTokenEnv: "", APIKeyEnv: "stored-key", AuthTokenEnv: ""}},
		{"api-key exported wins over the stored one", engine.AuthAPIKey, everyCredentialExported,
			map[string]string{OAuthTokenEnv: "", APIKeyEnv: "shell-key", AuthTokenEnv: ""}},
		{"login shares the storage the launching env resolves, every credential blanked", engine.AuthLogin,
			map[string]string{OAuthTokenEnv: "shell-token", APIKeyEnv: "shell-key", AuthTokenEnv: "shell-gateway", ConfigDirEnv: "/h/./cfg/"},
			map[string]string{SecureStorageEnv: "/h/./cfg/", OAuthTokenEnv: "", APIKeyEnv: "", AuthTokenEnv: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := claudeAuth{}.LaunchEnv(tc.mode, shellOf(tc.shell), stored)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// An empty export is no credential: it neither wins nor blanks the store.
func TestClaudeAuth_LaunchEnv_AnEmptyExportIsNotAValue(t *testing.T) {
	got, err := claudeAuth{}.LaunchEnv(engine.AuthToken, shellOf(map[string]string{OAuthTokenEnv: ""}), memStore{engine.AuthToken: "stored"})
	require.NoError(t, err)
	assert.Equal(t, "stored", got[OAuthTokenEnv])
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
			got, err := claudeAuth{}.LaunchEnv(engine.AuthLogin, shellOf(tc.env), memStore{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got[SecureStorageEnv])
		})
	}
}

// A non-login mode never touches the storage var: "" would mean HOME/.claude,
// the human's own credential, so blanking it would point there.
func TestClaudeAuth_LaunchEnv_NonLoginLeavesTheStorageVarAlone(t *testing.T) {
	for _, mode := range []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey} {
		got, err := claudeAuth{}.LaunchEnv(mode, shellOf(nil), memStore{engine.AuthToken: "t", engine.AuthAPIKey: "k"})
		require.NoError(t, err)
		assert.NotContains(t, got, SecureStorageEnv, mode)
	}
}

// Nothing exported and nothing stored is ErrNoCredential, the typed signal a
// run mints on (or refuses on, unattended).
func TestClaudeAuth_LaunchEnv_NoCredentialIsTyped(t *testing.T) {
	_, err := claudeAuth{}.LaunchEnv(engine.AuthToken, shellOf(nil), memStore{})
	require.ErrorIs(t, err, engine.ErrNoCredential)
	assert.ErrorContains(t, err, OAuthTokenEnv)
}

func TestClaudeAuth_LaunchEnv_RefusesAModeItLacks(t *testing.T) {
	_, err := claudeAuth{}.LaunchEnv("keychain", shellOf(nil), memStore{})
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
	for _, mode := range []engine.AuthMode{engine.AuthAPIKey, engine.AuthLogin} {
		_, err := claudeAuth{binary: "/nonexistent"}.Mint(context.Background(), mode, engine.Terminal{})
		require.ErrorIs(t, err, engine.ErrMintUnsupported, mode)
	}
}
