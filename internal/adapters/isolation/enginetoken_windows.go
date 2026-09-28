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
// read-only attribute, and a credential would be as private as whatever ACL
// its directory inherited. The directory therefore gets a PROTECTED DACL
// (nothing inherited from above) holding one inheritable ACE for the
// current user. The credential file is created inside it by
// iox.WriteFileAtomic and inherits exactly that ACE; StoreEngineCredential
// then checks the file, so an inheritance that did not happen fails the
// store.
//
// Reading tolerates SYSTEM and the Administrators group beside the owner
// (ruled 2026-09-25): both can take any file on the machine regardless, so
// an ACL granting them is still owner-only in every sense that matters, and
// refusing it would refuse the ACLs Windows itself writes by default.

// restrictCredentialDir replaces dir's DACL with a protected, owner-only one.
func restrictCredentialDir(dir string) error {
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

// ownerOnlyViolation says why p is open beyond the current user and the
// tolerated principals (toleratedSIDs), or "" when it is not. Deny ACEs
// narrow access and are ignored; any other ACE type is refused rather than
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
	grantees, why, err := daclGrantees(dacl)
	if why != "" || err != nil {
		return why, err
	}
	tolerated, err := toleratedSIDs()
	if err != nil {
		return "", err
	}
	return aclExposure(sid.String(), tolerated, grantees), nil
}

// daclGrantees are the SIDs dacl's allow ACEs grant, as strings. Deny ACEs
// narrow access and are skipped; any other ACE type is not interpreted, and
// why says so.
func daclGrantees(dacl *windows.ACL) (grantees []string, why string, err error) {
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, "", fmt.Errorf("read ACE %d: %w", i, err)
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			grantees = append(grantees, aceSID(ace).String())
		default:
			return nil, fmt.Sprintf("carries an ACE of type %d that is not checked", ace.Header.AceType), nil
		}
	}
	return grantees, "", nil
}

// toleratedSIDs are the principals an owner-only ACL may also grant: the
// machine's SYSTEM account and its Administrators group.
func toleratedSIDs() ([]string, error) {
	var out []string
	for _, k := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		s, err := windows.CreateWellKnownSid(k)
		if err != nil {
			return nil, fmt.Errorf("resolve a well-known SID: %w", err)
		}
		out = append(out, s.String())
	}
	return out, nil
}

// describeProtection is what `auth status` shows of a stored credential on
// Windows, where a mode says nothing: the ACL verdict, owner-only or
// exposed and to whom (ruled 2026-09-25).
func describeProtection(p string, info fs.FileInfo) (string, error) {
	why, err := ownerOnlyViolation(p, info)
	if err != nil {
		return "", err
	}
	if why == "" {
		return "owner-only", nil
	}
	return "exposed: " + why, nil
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
