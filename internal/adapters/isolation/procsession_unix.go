//go:build !windows

package isolation

import (
	"os"
	"os/exec"
	"syscall"
)

// isolateRunner decides the process attributes of the host runner subprocess
// StartHostRunner launches (`ctxloom runner <engine>`). Two things, for
// opposite directions of the lifetime:
//
//  1. Setsid — the runner leads a FRESH session (session id == its own pid) so
//     killSession can later reap its entire subtree, including a grandchild the
//     runner itself puts in a SEPARATE process group, as one unit, without
//     touching anything outside that dedicated session. This is the DOWNWARD
//     guarantee: when teardown runs, it reaches everything. See killSession.
//
//  2. Pdeathsig — the kernel signals the runner the instant its host process
//     dies, however it dies. This is the UPWARD guarantee, and it exists
//     because (1) only works while the host is alive to run it:
//     HostRunner.Kill lives INSIDE the host, so a SIGKILL, an OOM kill, `go
//     test -timeout`'s escalation, a panic that skipped every defer, a cobra
//     path that called os.Exit, or a killed shell that took its whole process
//     group down leaves the runner with nothing watching it. And the runner
//     cannot notice on its own: (1) has by construction put it out of reach
//     of any process-group signal. After the host is gone the kernel is the
//     only party that still remembers the relationship — hence
//     PR_SET_PDEATHSIG rather than more userspace bookkeeping. Without it,
//     orphaned runners accumulate.
//
// The signal is SIGTERM, not SIGKILL, so the runner unwinds through its own
// signal-aware context (cli's runner command) and ends the engine process it
// drives before it goes. PR_SET_PDEATHSIG is Linux-only (see pdeath_other.go);
// elsewhere the runner arms the same guarantee from the inside
// (parentwatch.WithParent), which covers our own runner but not a foreign
// binary.
func isolateRunner(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	setRunnerPdeathsig(cmd.SysProcAttr)
}

// askToStop is how HostRunner.Kill asks a runner to end: SIGTERM, which the
// runner's signal context turns into its orderly teardown.
func askToStop(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
