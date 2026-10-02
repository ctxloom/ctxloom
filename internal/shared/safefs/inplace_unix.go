//go:build !windows

package safefs

import (
	"errors"
	"os"
	"syscall"
)

// openNoFollow makes the kernel itself refuse to open a symlink, rather than
// asking it first and opening second. An Lstat-then-open check is racy: the
// path it inspected and the path it opens are resolved at two different
// moments, and anything that can plant a symlink in between wins. O_NOFOLLOW
// collapses both into one syscall, so there is no window to win.
const openNoFollow = syscall.O_NOFOLLOW

// refuseSymlinkDest is a no-op here: the open flags already carry the
// refusal. The seam exists for Windows, where they cannot — see
// inplace_windows.go.
func refuseSymlinkDest(string) error { return nil }

// isSymlinkOpenErr reports whether err is the kernel refusing O_NOFOLLOW on a
// symlink. Linux reports ELOOP; the BSDs report EMLINK for this specific
// case, which is why both are named rather than just the familiar one.
func isSymlinkOpenErr(err error) bool {
	return errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.EMLINK) || errors.Is(err, os.ErrInvalid) && false
}
