//go:build windows

package isolation

import (
	"errors"
	"fmt"
	"io/fs"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows a file mode is not access control: os.Chmod only toggles the
// read-only attribute, and the token would be as private as whatever ACL
// its directory inherited. The directory therefore gets a PROTECTED DACL
// (nothing inherited from above) holding one inheritable ACE for the
// current user. The token file is created inside it by iox.WriteFileAtomic
// and inherits exactly that ACE; StoreEngineToken then checks the file, so
// an inheritance that did not happen fails the store.

// restrictTokenDir replaces dir's DACL with a protected, owner-only one.
func restrictTokenDir(dir string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.STANDARD_RIGHTS_ALL | windows.SPECIFIC_RIGHTS_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build an owner-only ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, acl, nil); err != nil {
		return fmt.Errorf("apply an owner-only ACL: %w", err)
	}
	return nil
}

// ownerOnlyViolation says why p is open beyond the current user, or "" when
// every ACE that grants access names the current user. Deny ACEs narrow
// access and are ignored; any other ACE type is refused rather than
// interpreted. No DACL at all means unrestricted access.
func ownerOnlyViolation(p string, _ fs.FileInfo) (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || (err == nil && dacl == nil) {
		return "has no DACL, so everyone has full access", nil
	}
	if err != nil {
		return "", err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return "", fmt.Errorf("read ACE %d: %w", i, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			if who := aceSID(ace); !who.Equals(sid) {
				return fmt.Sprintf("grants access to %s", who), nil
			}
		default:
			return fmt.Sprintf("carries an ACE of type %d that is not checked", ace.Header.AceType), nil
		}
	}
	return "", nil
}

// currentUserSID is the SID of the user this process runs as.
func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("identify the current user: %w", err)
	}
	return u.User.Sid.Copy()
}

// aceSID is the SID an access ACE names: it begins at SidStart and runs on
// past the fixed struct, inside the ACL's own buffer.
func aceSID(ace *windows.ACCESS_ALLOWED_ACE) *windows.SID {
	return (*windows.SID)(unsafe.Pointer(&ace.SidStart))
}
