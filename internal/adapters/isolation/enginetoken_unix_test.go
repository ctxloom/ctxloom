//go:build !windows

package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// On unix owner-only is a mode: 0600 for the token, 0700 for its directory.
func TestStoreEngineToken_UnixModes(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())
}

// A token anyone but its owner can reach is not exported: the refusal is
// ErrTokenExposed, names the loose path, and leaves the var unset.
func TestExportStoredTokens_RefusesATokenLooserThanOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(tokenPath string) string
		mode   os.FileMode
	}{
		{"world-readable file", func(p string) string { return p }, 0o644},
		{"group-readable file", func(p string) string { return p }, 0o640},
		{"world-listable dir", filepath.Dir, 0o755},
		{"group-traversable dir", filepath.Dir, 0o710},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenHome(t)
			path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
			require.NoError(t, err)
			loose := tc.target(path)
			require.NoError(t, os.Chmod(loose, tc.mode))

			err = ExportStoredTokens()
			require.ErrorIs(t, err, ErrTokenExposed)
			assert.Contains(t, err.Error(), loose)
			assert.Contains(t, err.Error(), "ctxloom auth set-token")
			_, set := os.LookupEnv(claude.OAuthTokenEnv)
			assert.False(t, set, "a refused token never reaches the env")
		})
	}
}

// Re-storing is the fix the refusal names: it tightens a loosened directory
// and writes a fresh owner-only file, after which the token is read.
func TestStoreEngineToken_ReStoringRepairsALooseToken(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	require.NoError(t, os.Chmod(path, 0o644))
	require.ErrorIs(t, ExportStoredTokens(), ErrTokenExposed)

	_, err = StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	require.NoError(t, ExportStoredTokens())
	assert.Equal(t, fixtureToken, os.Getenv(claude.OAuthTokenEnv))
}
