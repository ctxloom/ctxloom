//go:build !windows

package testenv

import (
	"os"
	"os/exec"
	"syscall"
)

// ownProcessGroup makes cmd the leader of a new process group, so everything
// it spawns can be killed as one unit. It amends whatever SysProcAttr the
// caller already set (Command sets the parent-death signal) rather than
// replacing it.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the whole group p leads. Best effort: a group
// that already exited is the outcome the caller wants either way.
func killProcessGroup(p *os.Process) {
	_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
}
