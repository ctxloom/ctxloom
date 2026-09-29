//go:build windows

package fileperm

import (
	"os"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// ownerWrite is the one permission bit Windows stores: the read-only
// attribute, which os reports as the absence of the write bits. Every other
// bit of a Windows FileMode is synthesized (0666/0777, or 0444/0555).
const ownerWrite os.FileMode = 0o200

// Equal asserts got is writable exactly when want is.
func Equal(t testing.TB, want, got os.FileMode, msgAndArgs ...any) bool {
	t.Helper()
	return assert.Equal(t, want&ownerWrite, got&ownerWrite, msgAndArgs...)
}

// OwnerOnly asserts p has a DACL whose every ACE is an allow ACE naming the
// current user: a mode is not access control on NTFS, the DACL is.
func OwnerOnly(t testing.TB, p string) bool {
	t.Helper()
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	me := tu.User.Sid
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	dacl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, dacl, "%s has no DACL, so everyone has full access", p)
	ok := assert.NotZero(t, dacl.AceCount, "%s grants its owner nothing", p)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		require.NoError(t, windows.GetAce(dacl, i, &ace))
		ok = assert.Equal(t, uint8(windows.ACCESS_ALLOWED_ACE_TYPE), ace.Header.AceType, "%s ACE %d", p, i) && ok
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		ok = assert.True(t, sid.Equals(me), "%s ACE %d names %s, not the owner %s", p, i, sid, me) && ok
	}
	return ok
}
