//go:build !windows

package owneronly

import (
	"fmt"
	"io/fs"
	"os"
)

func restrict(dir string) error {
	return os.Chmod(dir, DirMode)
}

// violation says why p is open beyond its owner, or "" when no group or
// other permission bit is set.
func violation(_ string, info fs.FileInfo) (string, error) {
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Sprintf("has mode %04o", perm), nil
	}
	return "", nil
}
