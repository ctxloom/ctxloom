// Package exitstatus is the ONE computation of the exit status a finished
// child process reports. Every path that runs an engine — the pty, the
// non-interactive launch, the structured driver, the originator's pty session
// — reports a child's end through it, so the status a caller sees does not
// depend on which path happened to run the engine.
package exitstatus

import (
	"errors"
	"os/exec"
	"syscall"
)

// signaledStatus is the portable slice of the platform wait status this
// package needs. os/exec exposes it as ProcessState.Sys(), typed
// syscall.WaitStatus — a per-GOOS struct, so asserting the concrete type would
// only build on unix. Both shapes carry these two methods, so an interface
// assertion compiles everywhere and needs no build-tagged file.
type signaledStatus interface {
	Signaled() bool
	Signal() syscall.Signal
}

// Of maps a finished child's *exec.ExitError to a valid POSIX exit status.
// os/exec reports -1 for a process that died on a signal, and -1 is not an
// exit status a process can carry: propagated to os.Exit the OS truncates it
// to 255, making a killed engine indistinguishable from an engine that really
// exited 255 and from a runner-internal failure. Shells have long since settled
// this — a signalled child reports 128+signum (130 SIGINT, 137 SIGKILL, 143
// SIGTERM) — and ctxloom's own launch path is a transparent wrapper around the
// engine's status, so it reports what a shell would: the status rides the
// run's Result (exit_code) and an interactive `ctxloom run` exits with it.
//
// WINDOWS, stated rather than left implicit: its syscall.WaitStatus satisfies
// the interface above but hard-codes Signaled() to false and Signal() to -1,
// because Windows has no wait status that distinguishes "killed by a signal"
// from an ordinary exit code — TerminateProcess just sets the exit code. So
// the assertion succeeds, the guard is false, and the ExitCode() fallthrough
// runs. There is nothing to map there; when there is nothing to map, the right
// answer is to say so, not to invent a synthetic signal.
func Of(exitErr *exec.ExitError) int {
	if status, ok := exitErr.Sys().(signaledStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return exitErr.ExitCode()
}

// Error is a finished child's exit already classified: Code is its POSIX exit
// status (Of), Err the error that reported the exit, whose text Error keeps.
// It is how the adapter that owns a process hands a consumer that must not
// reach os/exec — the coordinator, which reads it through its own
// ExitStatus() interface — a status it can record as-is.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }

// Unwrap exposes the reporting error, so errors.As still finds the
// *exec.ExitError (or wait failure) beneath.
func (e *Error) Unwrap() error { return e.Err }

// ExitStatus is the classified exit status.
func (e *Error) ExitStatus() int { return e.Code }

// Classify returns err carrying the classified status of the *exec.ExitError
// in its chain (an *Error wrapping err); nil, and an error with no process
// status at all (a failed reap), come back unchanged.
func Classify(err error) error {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return err
	}
	return &Error{Code: Of(ee), Err: err}
}
