//go:build windows

package isolation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// On Windows owner-only is a DACL: the token directory's is protected (it
// inherits nothing from above) and it and the file grant only the current
// user.
func TestStoreEngineToken_AppliesAnOwnerOnlyDACL(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
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
}

// A token whose file or directory grants anyone else access is refused.
func TestExportStoredTokens_RefusesATokenOthersCanRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target func(tokenPath string) string
	}{
		{"file", func(p string) string { return p }},
		{"dir", filepath.Dir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokenHome(t)
			path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
			require.NoError(t, err)
			loose := tc.target(path)
			grantEveryoneRead(t, loose)

			err = ExportStoredTokens()
			require.ErrorIs(t, err, ErrTokenExposed)
			assert.Contains(t, err.Error(), loose)
			_, set := os.LookupEnv(claude.OAuthTokenEnv)
			assert.False(t, set, "a refused token never reaches the env")
		})
	}
}

// When the owner-only ACL cannot be applied the store fails and writes no
// token. The directory is pre-made with an OWNER RIGHTS ACE granting only
// read and traverse, which withdraws the owner's implicit WRITE_DAC.
func TestStoreEngineToken_FailsWhenTheACLCannotBeApplied(t *testing.T) {
	tokenHome(t)
	path, err := StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	dir := filepath.Dir(path)

	ownerRights, err := windows.CreateWellKnownSid(windows.WinCreatorOwnerRightsSid)
	require.NoError(t, err)
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_TRAVERSE,
		AccessMode:        windows.SET_ACCESS,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
			TrusteeValue: windows.TrusteeValueFromSID(ownerRights),
		},
	}}, nil)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil))

	_, err = StoreEngineToken(claude.EngineName, []byte(fixtureToken))
	require.Error(t, err)
	assert.Contains(t, err.Error(), dir)
	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "no token is written when the ACL is not applied")
}

// grantEveryoneRead adds an Everyone read ACE to p's existing DACL.
func grantEveryoneRead(t *testing.T, p string) {
	t.Helper()
	everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
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
			TrusteeValue: windows.TrusteeValueFromSID(everyone),
		},
	}}, current)
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(p, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil))
}
