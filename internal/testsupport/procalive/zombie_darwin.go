package procalive

import "golang.org/x/sys/unix"

// sZOMB is darwin's zombie process state (SZOMB in <sys/proc.h>); x/sys/unix
// does not export it.
const sZOMB = 5

// isZombie reads pid's state from the kernel's process table (kern.proc.pid),
// darwin having no /proc; the table lists zombies until they are reaped. A
// failed lookup (the pid is already gone) is "cannot tell, so not a zombie",
// as on Linux: Alive's kill(pid, 0) decides existence.
func isZombie(pid int) bool {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false
	}
	return kp.Proc.P_stat == sZOMB
}
