//go:build windows

package procsig

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// Interrupt asks p to end what it is doing: CTRL_BREAK_EVENT to p's process
// group, which SpawnAttr made p the root of. BEST EFFORT, and ruled so: the
// event reaches only a process sharing the caller's console, and Node maps it
// to SIGBREAK rather than SIGINT — whether claude then ends the turn with a
// result is proven only on the Windows CI job. The caller kills after its
// grace regardless.
func Interrupt(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}

// Stop ends p outright: os.Process.Signal delivers only Kill on Windows, so
// there is no request a process could honour.
func Stop(p *os.Process) error { return p.Kill() }

// SpawnAttr starts the child as the root of a new process group — the target
// Interrupt's CTRL_BREAK_EVENT is addressed to.
func SpawnAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}
