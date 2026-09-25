//go:build !windows

package isolation

import (
	"fmt"
	"io/fs"
	"os"
)

// restrictTokenDir makes dir owner-only. It is re-applied on every store, so
// a directory loosened after the fact is tightened by the next store.
func restrictTokenDir(dir string) error {
	return os.Chmod(dir, tokenDirMode)
}

// ownerOnlyViolation says why p is open beyond its owner, or "" when no
// group or other permission bit is set.
func ownerOnlyViolation(_ string, info fs.FileInfo) (string, error) {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("has mode %04o", perm), nil
	}
	return "", nil
}
