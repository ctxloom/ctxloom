//go:build !windows

package isolation

import (
	"fmt"
	"io/fs"
	"os"
)

// restrictCredentialDir makes dir owner-only. It is re-applied on every
// store, so a directory loosened after the fact is tightened by the next
// store.
func restrictCredentialDir(dir string) error {
	return os.Chmod(dir, credentialDirMode)
}

// ownerOnlyViolation says why p is open beyond its owner, or "" when no
// group or other permission bit is set.
func ownerOnlyViolation(_ string, info fs.FileInfo) (string, error) {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("has mode %04o", perm), nil
	}
	return "", nil
}

// describeProtection is what `auth status` shows of a stored credential on
// unix: its mode, which says who can read it.
func describeProtection(_ string, info fs.FileInfo) (string, error) {
	return fmt.Sprintf("mode %04o", info.Mode().Perm()), nil
}
