package grpc

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// procHandle pins ONE process's identity for the length of a sweep step. The
// sweep decides whether to kill a pid by reading /proc/<pid>/stat and then
// signals it, and a pid is not a stable identifier across those two steps: the
// target can exit in between and the kernel can hand its number to an unrelated
// process, which would then take the SIGKILL.
//
// On Linux a pidfd closes that window. The fd is taken FIRST and refers to the
// process that existed at that moment, forever — if the target exits and its pid
// is reused, the fd still names the original, so the signal lands on a dead
// process (ESRCH) rather than on the newcomer. The /proc read that follows may
// then describe the wrong process, but the worst outcome is a skipped kill or a
// no-op signal, never a stranger's death.
type procHandle struct{ fd int }

// pinProcess takes a handle on pid. It reports false when the process is
// already gone or cannot be pinned, in which case there is nothing to kill.
func pinProcess(pid int) (procHandle, bool) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return procHandle{fd: -1}, false
	}
	return procHandle{fd: fd}, true
}

// kill SIGKILLs the pinned process, then releases the handle. Best-effort: an
// already-dead target is the outcome the caller wanted anyway.
func (h procHandle) kill() {
	_ = unix.PidfdSendSignal(h.fd, syscall.SIGKILL, nil, 0)
	h.close()
}

// close releases the handle without signalling.
func (h procHandle) close() {
	if h.fd >= 0 {
		_ = unix.Close(h.fd)
	}
}
