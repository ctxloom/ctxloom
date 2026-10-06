//go:build windows

package safefs

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// loosen grants Everyone read on dir, which on Windows is exposure.
func loosen(t *testing.T, dir string) {
	t.Helper()
	grantWellKnownRead(t, dir, windows.WinWorldSid)
}

// ownerOnlyFixture is an Ensure'd directory holding one file.
func ownerOnlyFixture(t *testing.T) (dir, file string) {
	t.Helper()
	root := New()
	dir = filepath.Join(t.TempDir(), "private")
	require.NoError(t, root.Private.Ensure(dir))
	file = filepath.Join(dir, "secret")
	require.NoError(t, WriteFile(root.Fs, file, []byte("x"), PrivateFileMode))
	return dir, file
}

// An exposed directory is restricted to a PROTECTED DACL: it inherits nothing
// from above, so a permissive parent cannot reach in again, and what is
// created inside afterwards inherits the owner's ACE.
func TestNewPrivate_EnsureProtectsAnExposedDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	root := New()
	require.NoError(t, root.Private.Ensure(dir))
	loosen(t, dir)
	require.NoError(t, root.Private.Ensure(dir))

	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	control, _, err := sd.Control()
	require.NoError(t, err)
	assert.NotZero(t, control&windows.SE_DACL_PROTECTED, "the directory inherits nothing")
	require.NoError(t, root.Private.Check(dir))
}

// SYSTEM and Administrators beside the owner are tolerated (ruled
// 2026-09-25): such a path passes the check.
func TestNewPrivate_CheckToleratesSystemAndAdministrators(t *testing.T) {
	dir, f := ownerOnlyFixture(t)
	grantWellKnownRead(t, f, windows.WinLocalSystemSid)
	grantWellKnownRead(t, f, windows.WinBuiltinAdministratorsSid)

	require.NoError(t, New().Private.Check(dir, f))
}

// Anyone else granted access is exposure, refused as an *ExposedError, and
// the refusal names who.
func TestNewPrivate_CheckRefusesAPathOthersCanRead(t *testing.T) {
	for _, target := range []string{"file", "dir"} {
		t.Run(target, func(t *testing.T) {
			dir, f := ownerOnlyFixture(t)
			loose := f
			if target == "dir" {
				loose = dir
			}
			grantWellKnownRead(t, loose, windows.WinWorldSid)

			err := New().Private.Check(dir, f)
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
