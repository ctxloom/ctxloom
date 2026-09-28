package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const fixtureToken = "sk-ant-oat01-fixture"

// tokenHome isolates the environment (a fresh home, every engine credential
// var unset), so a test reads only the store it wrote.
func tokenHome(t *testing.T) string {
	t.Helper()
	home := testsupport.Isolate(t)
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

// A token that exists only in the store reaches both launch paths once
// exported: a host runner's env (os.Environ laid under the spawn env), and a
// container's auth plan, by name. This is what lets a claude child launch
// whatever engine its owner runs.
func TestStoredToken_ReachesTheHostRunnerAndTheContainerPassthrough(t *testing.T) {
	tokenHome(t)
	for _, v := range claudeAuth(t).EnvTriggers {
		t.Setenv(v, "")
	}
	require.NoError(t, os.Unsetenv(claude.OAuthTokenEnv))
	_, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	require.NoError(t, ExportStoredTokens())

	cmd, err := hostRunnerCmd([]string{"claude-code"}, map[string]string{"CTXLOOM_HARP": "brave-warm-otter"})
	require.NoError(t, err)
	assert.Contains(t, cmd.Env, claude.OAuthTokenEnv+"="+fixtureToken, "the host runner inherits the exported token")
	for _, a := range cmd.Args {
		assert.NotContains(t, a, fixtureToken, "the token never rides argv")
	}

	plan, ok := resolveDeclaredAuth(claudeAuth(t))
	require.True(t, ok)
	assert.Contains(t, plan.envPassthrough, claude.OAuthTokenEnv, "the container gets it by name")
}
