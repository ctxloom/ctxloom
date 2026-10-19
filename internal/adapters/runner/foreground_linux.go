//go:build linux

package runner

import (
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

// runOnTerminal runs cmd, whose standard streams are tty, and returns what
// cmd.Run would. When tty is the caller's controlling terminal, cmd runs as
// the terminal's foreground job: in a process group of its own that the
// terminal makes its foreground, so the signals the terminal raises for a
// typed ^C, ^\ or ^Z — while cmd's line discipline is in effect — reach cmd's
// group and never the caller, which shares cmd's session but not its group.
// A tty that is not the caller's controlling terminal raises no signal for
// the caller, and cmd runs as cmd.Run runs it.
//
// The caller has no job control to offer cmd — no prompt to return to, no
// `fg` — so a cmd stopped by a ^Z, or by suspending its own group, is
// continued at once, its whole group with it. The kernel discarded those
// stops while an engine led a session of its own; a foreground job whose
// parent shares its session is stopped by them, and nothing else would wake
// it.
//
// Once cmd has exited, whatever it left in its group is hung up, as the
// kernel hangs up a session's foreground job when the session's leader exits,
// and the terminal goes back to the group that held it. The hangup is the
// caller's to send: with the terminal back, cmd's group is no longer the
// foreground job the caller's own exit would hang up. A failure to take the
// terminal back is not reported: the caller ends with its engine, and until
// it does only what is typed is lost.
func runOnTerminal(cmd *exec.Cmd, tty *os.File) error {
	fd := int(tty.Fd())
	holder, err := unix.IoctlGetUint32(fd, unix.TIOCGPGRP)
	if err != nil {
		return cmd.Run()
	}
	// Foreground implies Setpgid: the group cmd leads is its own.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = fd

	// Armed before the start, so a stop that comes before the loop below
	// reads its channel is still a SIGCHLD waiting there.
	childEvents := make(chan os.Signal, 1)
	signal.Notify(childEvents, syscall.SIGCHLD)
	defer signal.Stop(childEvents)
	// The child takes the terminal before it execs, so a start that fails
	// after the fork has moved it too.
	defer giveTerminal(fd, int(holder))
	if err := cmd.Start(); err != nil {
		return err
	}
	job := cmd.Process.Pid
	exited := make(chan error)
	go func() { exited <- cmd.Wait() }()
	for {
		select {
		case <-childEvents:
			continueIfStopped(job)
		case err := <-exited:
			_ = unix.Kill(-job, unix.SIGHUP)
			return err
		}
	}
}

// continueIfStopped continues job's process group when job is stopped. The
// query reports a stop and nothing else, so job's exit is left to its reaper
// (cmd.Wait). si_signo is SIGCHLD only when the query reported a stop: it is
// left zero when there is none, and when there is no job left to ask about.
func continueIfStopped(job int) {
	var info unix.Siginfo
	_ = unix.Waitid(unix.P_PID, job, &info, unix.WSTOPPED|unix.WNOHANG, nil)
	if info.Signo == int32(unix.SIGCHLD) {
		_ = unix.Kill(-job, unix.SIGCONT)
	}
}

// giveTerminal makes pgrp tty's foreground process group. The caller is in
// the background when it takes its terminal back, and the kernel answers a
// background tcsetpgrp with SIGTTOU (which stops the caller) or, in an
// orphaned group, EIO — unless SIGTTOU is blocked. It is blocked on this
// thread alone, so nothing another goroutine starts meanwhile inherits it.
// Changing the mask fails only on an argument these calls never pass.
func giveTerminal(fd, pgrp int) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var block, was unix.Sigset_t
	block.Val[0] = 1 << (unix.SIGTTOU - 1)
	_ = unix.PthreadSigmask(unix.SIG_BLOCK, &block, &was)
	defer func() { _ = unix.PthreadSigmask(unix.SIG_SETMASK, &was, nil) }()
	_ = unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgrp)
}
