//go:build !windows

package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// On unix owner-only is a mode: 0600 for the credential, 0700 for its
// directory; status shows the mode.
func TestStoreEngineCredential_UnixModes(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dir, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dir.Mode().Perm())

	st, err := credentialStatus(claude.EngineName, engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, "mode 0600", st.Protection)
}

// A credential anyone but its owner can reach is not read: the refusal is
// ErrCredentialExposed, names the loose path and the fix, and is never
// mistaken for "nothing stored".
func TestStoredCredentials_RefusesACredentialLooserThanOwnerOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(path string) string
		mode   os.FileMode
	}{
		{"world-readable file", func(p string) string { return p }, 0o644},
		{"group-readable file", func(p string) string { return p }, 0o640},
		{"world-listable dir", filepath.Dir, 0o755},
		{"group-traversable dir", filepath.Dir, 0o710},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenHome(t)
			path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
			require.NoError(t, err)
			loose := tc.target(path)
			require.NoError(t, os.Chmod(loose, tc.mode))

			_, err = StoredCredentials(claude.EngineName).Read(engine.AuthToken)
			require.ErrorIs(t, err, ErrCredentialExposed)
			assert.NotErrorIs(t, err, engine.ErrNoCredential, "an exposed credential is refused, never treated as absent (which would mint over it)")
			assert.Contains(t, err.Error(), loose)
			assert.Contains(t, err.Error(), "ctxloom auth mint")
			assert.NotContains(t, err.Error(), fixtureToken)
		})
	}
}

// Re-storing is the fix the refusal names: it tightens a loosened directory
// and writes a fresh owner-only file, after which the credential is read.
func TestStoreEngineCredential_ReStoringRepairsALooseCredential(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o755))
	require.NoError(t, os.Chmod(path, 0o644))
	store := StoredCredentials(claude.EngineName)
	_, err = store.Read(engine.AuthToken)
	require.ErrorIs(t, err, ErrCredentialExposed)

	_, err = StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	got, err := store.Read(engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, fixtureToken, string(got))
}
