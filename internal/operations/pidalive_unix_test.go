//go:build !windows

package operations

import (
	"os"
	"syscall"
)

// pidAlive reports whether pid names a live process, probed via signal 0 (no
// signal is actually delivered): ESRCH means the process is gone, EPERM still
// means it exists (just owned by someone else), and nil means it exists and
// is ours to signal.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
