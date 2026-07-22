//go:build !windows

package coord

import (
	"os"
	"syscall"
)

// PidAlive reports whether pid names a live process (signal-0 probe; EPERM
// still means alive).
func PidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
