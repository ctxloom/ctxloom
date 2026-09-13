//go:build windows

package iox

import (
	"fmt"
	"os"
)

// openNoFollow is zero on Windows: the platform has no O_NOFOLLOW, so the
// refusal cannot be made by the open syscall the way it is everywhere else
// (see inplace_unix.go).
const openNoFollow = 0

// refuseSymlinkDest is the Windows stand-in for O_NOFOLLOW: an explicit Lstat
// that refuses a symlinked destination before the open. It is STRICTLY WEAKER
// than the flag — the path is resolved twice, so a symlink planted between
// this check and the open is not caught — and it is here because a weaker
// refusal is better than none, not because the race is acceptable. Same
// defense, same message, documented degradation, in the same spirit as
// dirsync_windows.go's no-op.
func refuseSymlinkDest(path string) error {
	fi, err := os.Lstat(path)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("in-place write %s: destination is a symlink; refusing to write through it", path)
	}
	return nil
}

// isSymlinkOpenErr is always false on Windows: with no O_NOFOLLOW there is no
// symlink-specific open error to recognize; refuseSymlinkDest is what does
// the refusing.
func isSymlinkOpenErr(error) bool { return false }
