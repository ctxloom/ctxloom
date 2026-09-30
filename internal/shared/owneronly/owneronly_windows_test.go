//go:build windows

package owneronly

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// ownerOnlyFixture is an EnsureDir'd directory holding one file.
func ownerOnlyFixture(t *testing.T) (dir, file string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "private")
	require.NoError(t, EnsureDir(dir))
	file = filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(file, []byte("x"), FileMode))
	return dir, file
}

// On Windows the directory's DACL is PROTECTED: it inherits nothing from
// above, so a permissive parent (a user-profile temp dir grants SYSTEM and
// Administrators, a shared drive grants more) cannot reach in.
func TestEnsureDir_TheDACLIsProtected(t *testing.T) {
	dir, _ := ownerOnlyFixture(t)
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, control&windows.SE_DACL_PROTECTED, "the directory inherits nothing")
}

// SYSTEM and Administrators beside the owner are tolerated (ruled
// 2026-09-25): such a path passes the check.
func TestCheck_ToleratesSystemAndAdministrators(t *testing.T) {
	dir, f := ownerOnlyFixture(t)
	grantWellKnownRead(t, f, windows.WinLocalSystemSid)
	grantWellKnownRead(t, f, windows.WinBuiltinAdministratorsSid)

	require.NoError(t, Check(dir, f))
}

// Anyone else granted access is exposure, refused as an *ExposedError, and
// the refusal names who.
func TestCheck_RefusesAPathOthersCanRead(t *testing.T) {
	for _, target := range []string{"file", "dir"} {
		t.Run(target, func(t *testing.T) {
			dir, f := ownerOnlyFixture(t)
			loose := f
			if target == "dir" {
				loose = dir
			}
			grantWellKnownRead(t, loose, windows.WinWorldSid)

			err := Check(dir, f)
			var exposed *ExposedError
			require.True(t, errors.As(err, &exposed), "got %v", err)
			assert.Equal(t, loose, exposed.Path)
			assert.Equal(t, "grants access to S-1-1-0", exposed.Why)
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
