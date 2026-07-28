//go:build windows

package acp

import (
	"os/exec"
	"sync"
)

// setpgid is a no-op on Windows: syscall.SysProcAttr has no Setpgid field
// there (it is an entirely different struct shape), and Windows has no
// POSIX-style process-group kill primitive — the equivalent would be a Job
// Object, which is out of scope here (codex-acp's leaked worker is a *nix
// double-fork; this ACP transport is otherwise cross-platform, so it still
// builds and runs on Windows with today's single-process kill behavior).
func setpgid(cmd *exec.Cmd) {}

// killProcessGroup falls back to killing just the immediate process — today's
// pre-fix behavior — since there is no cheap unix-style process-group kill on
// Windows without Job Objects. This means a double-forked worker (the same
// codex-acp shape setpgid/killProcessGroup on unix exist to reap, see
// procgroup_unix.go) is knowingly leaked on Windows: its PPID reparents away
// and nothing here ever signals it. U011-F17 flagged that nothing told the
// operator; warn once per process so the leak is at least visible rather than
// silent (implementing a real Job Object, and whether ctxloom claims full
// Windows process-cleanup parity at all, are open product questions tracked
// separately, not decided by this warning).
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	warnWindowsProcessGroupLeakOnce()
	return cmd.Process.Kill()
}

var warnWindowsProcessGroupLeakOnce = sync.OnceFunc(func() {
	warnf("acp: killing engine subprocess on Windows only terminates the immediate process, not its process group -- a double-forked worker (e.g. codex-acp's) may be left running; see U011-F17")
})
