package procpin

import (
	"syscall"
	"time"
)

// ReadStat has no /proc to read here.
func ReadStat(int) (Stat, error) { return Stat{}, ErrUnsupported }

// Handle holds nothing on Windows: no process is pinned or signalled here.
type Handle struct{}

// Pin never pins on Windows, so no caller acts on another process here.
func Pin(int) (Handle, bool) { return Handle{}, false }

// Signal is never reached: Pin refuses on this platform.
func (Handle) Signal(syscall.Signal) error { return ErrUnsupported }

// WaitExit is never reached: Pin refuses on this platform.
func (Handle) WaitExit(time.Duration) bool { return false }

// Close is a no-op: nothing is held.
func (Handle) Close() {}
