//go:build windows

package isolation

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// On Windows owner-only is a DACL: the credential directory's is protected
// (it inherits nothing from above) and it and the file grant only the
// current user; status shows the ACL verdict, not a mode.
func TestStoreEngineCredential_AppliesAnOwnerOnlyDACL(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	me, err := currentUserSID()
	require.NoError(t, err)

	for _, p := range []string{filepath.Dir(path), path} {
		sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		require.NoError(t, err)
		dacl, _, err := sd.DACL()
		require.NoError(t, err)
		require.NotNil(t, dacl, "%s has a DACL", p)
		require.NotZero(t, dacl.AceCount, "%s grants its owner something", p)
		for i := uint32(0); i < uint32(dacl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			require.NoError(t, windows.GetAce(dacl, i, &ace))
			require.Equal(t, uint8(windows.ACCESS_ALLOWED_ACE_TYPE), ace.Header.AceType)
			assert.True(t, aceSID(ace).Equals(me), "%s ACE %d names %s", p, i, aceSID(ace))
		}
	}
	sd, err := windows.GetNamedSecurityInfo(filepath.Dir(path), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, control&windows.SE_DACL_PROTECTED, "the directory inherits nothing")

	st, err := credentialStatus(claude.EngineName, engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, "owner-only", st.Protection)
}

// SYSTEM and Administrators beside the owner are tolerated (ruled
// 2026-09-25): such a credential is read, and status still says owner-only.
func TestStoredCredentials_ToleratesSystemAndAdministrators(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
	require.NoError(t, err)
	grantWellKnownRead(t, path, windows.WinLocalSystemSid)
	grantWellKnownRead(t, path, windows.WinBuiltinAdministratorsSid)

	got, err := StoredCredentials(claude.EngineName).Read(engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, fixtureToken, string(got))
	st, err := credentialStatus(claude.EngineName, engine.AuthToken)
	require.NoError(t, err)
	assert.Equal(t, "owner-only", st.Protection)
}

// A credential whose file or directory grants anyone else access is
// refused, and status names who.
func TestStoredCredentials_RefusesACredentialOthersCanRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(tokenPath string) string
	}{
		{"file", func(p string) string { return p }},
		{"dir", filepath.Dir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenHome(t)
			path, err := StoreEngineCredential(claude.EngineName, engine.AuthToken, []byte(fixtureToken))
			require.NoError(t, err)
			loose := tc.target(path)
			grantWellKnownRead(t, loose, windows.WinWorldSid)

			_, err = StoredCredentials(claude.EngineName).Read(engine.AuthToken)
			require.ErrorIs(t, err, ErrCredentialExposed)
			assert.Contains(t, err.Error(), loose)
			if loose == path {
				st, err := credentialStatus(claude.EngineName, engine.AuthToken)
				require.NoError(t, err)
				assert.Contains(t, st.Protection, "exposed: grants access to S-1-1-0")
			}
		})
	}
}

// grantWellKnownRead adds a read ACE for the well-known principal to p's
// existing DACL.
func grantWellKnownRead(t *testing.T, p string, who windows.WELL_KNOWN_SID_TYPE) {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(who)
	require.NoError(t, err)
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	current, _, err := sd.DACL()
	require.NoError(t, err)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.FILE_GENERIC_READ,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, current)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil))
}
