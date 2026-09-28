package isolation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const fixtureToken = "sk-ant-oat01-fixture"

// tokenHome isolates the environment (a fresh home, every engine credential
// var unset), so a test reads only the store it wrote.
func tokenHome(t *testing.T) string {
	t.Helper()
	home := testsupport.Isolate(t)
	return home
}

// When the directory cannot be restricted to its owner the store fails,
// naming the directory, and writes no credential.
func TestStoreEngineCredential_FailsWhenTheDirCannotBeRestricted(t *testing.T) {
	tokenHome(t)
	refused := errors.New("restriction refused")
	prev := restrictDir
	restrictDir = func(string) error { return refused }
	t.Cleanup(func() { restrictDir = prev })

	_, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.ErrorIs(t, err, refused)
	path, perr := paths.HomeEngineCredentialPath(claude.EngineName, string(engine.AuthToken))
	require.NoError(t, perr)
	assert.Contains(t, err.Error(), filepath.Dir(path))
	assert.NoFileExists(t, path, "no credential is written when the directory is not restricted")
}

// A stored credential lands at the engine's per-mode path, owner-only by
// this platform's own check, and holds the credential without the newline a
// paste carries. What owner-only means per platform is asserted beside each
// twin.
func TestStoreEngineCredential_WritesAnOwnerOnlyFilePerMode(t *testing.T) {
	tokenHome(t)
	for _, mode := range []engine.AuthMode{engine.AuthToken, engine.AuthAPIKey} {
		path, err := StoreEngineCredential(claude.EngineName, mode, []byte(fixtureToken+"-"+string(mode)+"\n"))
		require.NoError(t, err)
		want, err := paths.HomeEngineCredentialPath(claude.EngineName, string(mode))
		require.NoError(t, err)
		assert.Equal(t, want, path)
		require.NoError(t, checkCredentialPrivate(path))
		got, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, fixtureToken+"-"+string(mode), string(got))
	}
}

func TestStoreEngineCredential_RefusesWhatIsNotOneCredential(t *testing.T) {
	tokenHome(t)
	_, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte("  \n"))
	assert.ErrorIs(t, err, ErrEmptyCredential)
	_, err = StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte("two words"))
	assert.ErrorIs(t, err, ErrMalformedCredential)
	_, err = StoreEngineCredential("no-such-engine", engine.AuthToken, []byte(fixtureToken))
	assert.ErrorIs(t, err, ErrNoAuth)
	_, err = StoreEngineCredential(claude.EngineName, "keychain", []byte(fixtureToken))
	assert.ErrorIs(t, err, engine.ErrUnknownAuthMode)
}

// The login and a cloud provider's variables are the human's own and are
// never stored by ctxloom; the refusal's remedy names the modes that are.
func TestStoreEngineCredential_RefusesTheModesItNeverStores(t *testing.T) {
	home := tokenHome(t)
	for _, mode := range []engine.AuthMode{engine.AuthLogin, engine.AuthCloud} {
		_, err := StoreEngineCredential(claude.EngineName, mode, []byte(fixtureToken))
		require.ErrorIs(t, err, ErrNotStored, mode)
		var r report.Remediable
		require.ErrorAs(t, err, &r)
		assert.Contains(t, r.Remedy(), "token, api-key", mode)
	}
	assert.NoDirExists(t, home+"/"+paths.AppDirName+"/"+paths.HomeAuthDirName, "nothing is created for a refused store")
}

// What is stored reads back per mode; nothing stored is the typed absence an
// engine's Credentials mints or refuses on.
func TestStoredCredentials_ReadsBackPerMode(t *testing.T) {
	tokenHome(t)
	store := StoredCredentials(claude.EngineName)
	_, err := store.Read(engine.AuthToken)
	require.ErrorIs(t, err, engine.ErrNoCredential)

	_, err = StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	got, err := store.Read(engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, fixtureToken, string(got))
	_, err = store.Read(engine.AuthAPIKey)
	require.ErrorIs(t, err, engine.ErrNoCredential, "a token is not a key")
}

// Status lists every stored-credential mode of every engine with auth, never
// the login, never the credential; a stored one carries this platform's
// protection verdict.
func TestEngineCredentialStatuses_ReportsStoredModesWithoutTheCredential(t *testing.T) {
	tokenHome(t)
	_, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	all, err := EngineCredentialStatuses()
	require.NoError(t, err)
	byMode := map[engine.AuthMode]EngineCredentialStatus{}
	for _, s := range all {
		if s.Engine == claude.EngineName {
			byMode[s.Mode] = s
		}
		assert.NotContains(t, s.Protection, fixtureToken)
	}
	require.Len(t, byMode, 2, "token and api-key; the login is never stored")
	assert.True(t, byMode[engine.AuthToken].Stored)
	assert.NotEmpty(t, byMode[engine.AuthToken].Protection)
	assert.False(t, byMode[engine.AuthAPIKey].Stored)
	assert.Empty(t, byMode[engine.AuthAPIKey].Protection)
}

// The Windows owner-only verdict, platform-neutrally: the owner and the
// tolerated machine principals (SYSTEM, Administrators) are not exposure;
// anyone else is, named once each.
func TestACLExposure(t *testing.T) {
	const owner, system, admins, everyone, users = "S-1-5-21-1", "S-1-5-18", "S-1-5-32-544", "S-1-1-0", "S-1-5-32-545"
	tolerated := []string{system, admins}
	for _, tc := range []struct {
		name     string
		grantees []string
		want     string
	}{
		{"owner only", []string{owner}, ""},
		{"owner with SYSTEM and Administrators is still owner-only", []string{owner, system, admins}, ""},
		{"no grantee at all", nil, ""},
		{"Everyone is exposure", []string{owner, everyone}, "grants access to " + everyone},
		{"each outsider named once, in ACL order", []string{users, owner, everyone, users}, "grants access to " + users + ", " + everyone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, aclExposure(owner, tolerated, tc.grantees))
		})
	}
}
