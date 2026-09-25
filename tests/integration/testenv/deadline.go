package testenv

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandBound is the wall-clock ceiling on ONE command the harness runs for
// a scenario (RunHistory.Exec — so TestEnvironment.Run — and RunWithStdin).
// Past it the command's process group is killed and the command fails with a
// *DeadlineError naming it, instead of the command running on until go test's
// own -timeout panics the whole binary and hides which scenario hung.
//
// Derivation: 5x the slowest ordinary command measured across both acceptance
// lanes. That was 3.9s, a `run --agent mock-container` launch against an
// already-built image in the @container lane; the hermetic lane's slowest
// was `doctor` at 2.4s over ~2,400 invocations, p99 0.5s. Image builds are
// NOT ordinary commands and are not measured here: they run before the
// scenarios, under their own bounds (the acceptance suite's suite images).
//
// Re-measure — do not just raise it — when it fires on a command that was not
// hung, or when a lane gains a kind of command slower than these: time every
// command in a focused run of each lane and take 3-5x the maximum. A bound
// raised by feel drifts back toward the whole-binary timeout it replaces.
const CommandBound = 20 * time.Second

// orphanGrace is how long a killed command gets to be reaped before its run
// is abandoned. SIGKILL of the group closes its pipes as the kernel reaps it,
// in milliseconds; the grace only separates "reaped" from "a descendant that
// left the group still holds the output open", which would otherwise block
// the wait forever and bring the hang straight back.
const orphanGrace = 5 * time.Second

// DeadlineError reports a command killed for outliving its bound. It names the
// command line and the bound, so the failing scenario says WHICH command hung
// and against WHAT ceiling.
type DeadlineError struct {
	// Command is the command line, binary by base name.
	Command string
	Bound   time.Duration
	// Orphaned is set when the command was not reaped within orphanGrace of
	// the kill: something outside its process group still held its output.
	Orphaned bool
}

func (e *DeadlineError) Error() string {
	msg := fmt.Sprintf("command `%s` did not exit within its %s bound; its process group was killed", e.Command, e.Bound)
	if e.Orphaned {
		msg += fmt.Sprintf(" — and it was still not reaped %s later, so a descendant outside the group holds its output open; the run was abandoned", orphanGrace)
	}
	return msg
}

// boundedRun is a started command whose wait is bounded.
type boundedRun struct {
	cmd  *exec.Cmd
	done chan error
}

// startBounded starts cmd as the leader of its own process group. The bound
// is armed separately, by wait, so a caller that holds the command's stdin
// open for a while first (RunWithStdin) does not spend the command's budget
// on its own hold.
func startBounded(cmd *exec.Cmd) (*boundedRun, error) {
	ownProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b := &boundedRun{cmd: cmd, done: make(chan error, 1)}
	go func() { b.done <- cmd.Wait() }()
	return b, nil
}

// wait returns cmd's own result if it exits within bound. Otherwise it kills
// the whole process group and returns a *DeadlineError.
//
// The GROUP, not the process: ctxloom's children (the llm plugin, a runner, a
// container client) inherit its stdout/stderr, and exec.Cmd.Wait does not
// return while any writer of those pipes is alive — killing only the direct
// child leaves the wait blocked on its grandchildren.
func (b *boundedRun) wait(bound time.Duration) error {
	timer := time.NewTimer(bound)
	defer timer.Stop()
	select {
	case err := <-b.done:
		return err
	case <-timer.C:
	}
	select {
	case err := <-b.done: // exited just as the bound elapsed
		return err
	default:
	}
	killProcessGroup(b.cmd.Process)
	de := &DeadlineError{Command: commandLine(b.cmd), Bound: bound}
	select {
	case <-b.done:
	case <-time.After(orphanGrace):
		de.Orphaned = true
	}
	return de
}

// runBounded starts cmd and waits for it under bound.
func runBounded(cmd *exec.Cmd, bound time.Duration) error {
	b, err := startBounded(cmd)
	if err != nil {
		return err
	}
	return b.wait(bound)
}

func commandLine(cmd *exec.Cmd) string {
	return strings.Join(append([]string{filepath.Base(cmd.Args[0])}, cmd.Args[1:]...), " ")
}
