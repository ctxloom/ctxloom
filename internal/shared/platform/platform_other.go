//go:build !linux && !darwin && !windows

package platform

import "github.com/ctxloom/ctxloom/internal/shared/platform/linux"

const (
	containersInVM = false
	loginShell     = true
	linuxHost      = false
	name           = "this platform"
)

// current is the Linux behaviour: the other hosts this builds for are
// freedesktop (XDG) systems with POSIX symbolic links.

var current Host = linux.OS{}
