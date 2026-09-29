//go:build !windows && !linux

package procpin

import (
	"errors"
	"syscall"
	"time"
)

// ReadStat has no /proc to read here.
func ReadStat(int) (Stat, error) { return Stat{}, ErrUnsupported }

// Handle is the non-Linux stand-in for the pidfd: there is no portable way to
// pin a process's identity here, so it keeps the pid and signals it directly.
type Handle struct{ pid int }

// Pin takes a handle on pid; it cannot tell a live process from a reused pid.
func Pin(pid int) (Handle, bool) { return Handle{pid: pid}, true }

// Signal sends sig to the pid. A process already gone is not an error.
func (h Handle) Signal(sig syscall.Signal) error {
	err := syscall.Kill(h.pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// WaitExit polls the pid for existence until timeout.
func (h Handle) WaitExit(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if errors.Is(syscall.Kill(h.pid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Close is a no-op: nothing is held.
func (Handle) Close() {}
