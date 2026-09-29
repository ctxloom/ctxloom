package procpin

import (
	"errors"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ReadStat reads pid's /proc/<pid>/stat.
func ReadStat(pid int) (Stat, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return Stat{}, err
	}
	return parseStat(data)
}

// Handle pins ONE process. It is a pidfd: if the process exits and its pid is
// reused, the fd still names the original, so a signal lands on a dead process
// (ESRCH) rather than on the newcomer.
type Handle struct{ fd int }

// Pin takes a handle on pid. It reports false when the process is already gone
// or cannot be pinned, in which case there is nothing to act on.
func Pin(pid int) (Handle, bool) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return Handle{fd: -1}, false
	}
	return Handle{fd: fd}, true
}

// Signal sends sig to the pinned process. A process that has already exited
// is not an error: it is the outcome any signal here is sent for.
func (h Handle) Signal(sig syscall.Signal) error {
	err := unix.PidfdSendSignal(h.fd, sig, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

// WaitExit waits up to timeout for the pinned process to exit and reports
// whether it did. A pidfd polls readable once its process has exited.
func (h Handle) WaitExit(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		ms := int(time.Until(deadline).Milliseconds())
		if ms < 0 {
			ms = 0
		}
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(h.fd), Events: unix.POLLIN}}, ms) //nolint:gosec // an fd fits in int32
		if err == nil {
			return n > 0
		}
		if !errors.Is(err, unix.EINTR) || ms == 0 {
			return false
		}
	}
}

// Close releases the handle without signalling.
func (h Handle) Close() {
	if h.fd >= 0 {
		_ = unix.Close(h.fd)
	}
}
