//go:build !linux && !darwin && !windows

package isolation

import "syscall"

// killSession on a unix that is neither Linux nor darwin reaches only the
// runner's OWN process group: isolateRunner made the runner a session leader,
// so its pid is also its pgid. x/sys/unix decodes the process table
// (kern.proc.all) only for darwin, so there is no way here to find members of
// the session that moved into a group of their own — a grandchild that called
// setpgid(2) escapes this sweep. These platforms are not release targets.
func killSession(sid int) {
	if sid <= 1 {
		return // never kill(-1): that is every process we may signal
	}
	_ = syscall.Kill(-sid, syscall.SIGKILL)
}
