package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

const fixtureToken = "sk-ant-oat01-fixture"

// tokenHome points the home at a fresh directory and clears the token var,
// so a test reads only the store it wrote.
func tokenHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(claude.OAuthTokenEnv, "")
	require.NoError(t, os.Unsetenv(claude.OAuthTokenEnv))
	t.Cleanup(resetTokenSources)
	return home
}

// The stored token is owner-only from the moment it exists, in an
// owner-only directory, and holds the token without the newline a paste
// carries.
func TestStoreEngineToken_WritesAnOwnerOnlyFile(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken+"\n"))
	require.NoError(t, err)
	want, err := paths.HomeEngineTokenPath(claude.EngineName)
	require.NoError(t, err)
	assert.Equal(t, want, path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, fixtureToken, string(got))
}

func TestStoreEngineToken_RefusesWhatIsNotOneToken(t *testing.T) {
	tokenHome(t)
	_, err := StoreEngineToken(claude.EngineName, []byte("  \n"))
	assert.ErrorIs(t, err, ErrEmptyToken)
	_, err = StoreEngineToken(claude.EngineName, []byte("two words"))
	assert.ErrorIs(t, err, ErrMalformedToken)
	_, err = StoreEngineToken("no-such-engine", []byte(fixtureToken))
	assert.ErrorIs(t, err, ErrNoTokenAuth)
}

// A stored token reaches the process env under the engine's token var, so
// every launch path, host or container, inherits it.
func TestExportStoredTokens_FillsAnUnsetVarFromTheStore(t *testing.T) {
	tokenHome(t)
	_, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	require.NoError(t, ExportStoredTokens())
	assert.Equal(t, fixtureToken, os.Getenv(claude.OAuthTokenEnv))

	st := engineTokenStatus(t, claude.EngineName)
	assert.Equal(t, TokenSourceStored, st.Source)
	assert.True(t, st.Stored)
}

// A token the user exported wins over the stored one.
func TestExportStoredTokens_AnExportedTokenWins(t *testing.T) {
	tokenHome(t)
	_, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	t.Setenv(claude.OAuthTokenEnv, "exported")
	require.NoError(t, ExportStoredTokens())
	assert.Equal(t, "exported", os.Getenv(claude.OAuthTokenEnv))
	assert.Equal(t, TokenSourceEnv, engineTokenStatus(t, claude.EngineName).Source)
}

func TestExportStoredTokens_NothingStoredLeavesTheVarUnset(t *testing.T) {
	tokenHome(t)
	require.NoError(t, ExportStoredTokens())
	_, set := os.LookupEnv(claude.OAuthTokenEnv)
	assert.False(t, set)
	st := engineTokenStatus(t, claude.EngineName)
	assert.Equal(t, TokenSourceNone, st.Source)
	assert.False(t, st.Stored)
}

func engineTokenStatus(t *testing.T, name string) EngineTokenStatus {
	t.Helper()
	all, err := EngineTokenStatuses()
	require.NoError(t, err)
	for _, s := range all {
		if s.Engine == name {
			return s
		}
	}
	t.Fatalf("no token status for %s", name)
	return EngineTokenStatus{}
}

// resetTokenSources forgets which vars this process filled from the store.
func resetTokenSources() {
	tokenSourcesMu.Lock()
	defer tokenSourcesMu.Unlock()
	storedExports = map[string]bool{}
}
