//go:build !windows

package acp

import (
	"errors"
	"os/exec"
	"syscall"
)

// setpgid configures cmd to start as the leader of its OWN, fresh process
// group (Setpgid with the zero-value Pgid means "use my own pid"), so
// killProcessGroup below can later reap the whole tree — not just the
// immediate child. This matters because codex-acp (and potentially other ACP
// agents) double-forks a worker process that survives its immediate parent
// (PPID reparents to 1) while staying in the SAME process group (it does not
// itself call setsid) — a plain Process.Kill on just the spawned pid never
// reaches that worker, leaking it for the lifetime of the host.
func setpgid(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup SIGKILLs the WHOLE process group cmd's process leads
// (negative pid is the kill(2) convention for "target the process group"),
// reaping the double-forked worker setpgid's own-group placement made
// reachable. ESRCH (no such process/group — already exited, or Setpgid
// somehow didn't apply) is not an error: the outcome ("nothing left to
// kill") is what the caller wants either way.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
