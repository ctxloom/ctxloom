package coord

import (
	"errors"
	"fmt"
	"time"
)

// defaultRunnerExitGrace bounds how long a run declared LOST waits for its
// runner process's exit to be observed before its end is recorded. A runner
// the kernel killed (an OOM kill) drops its channel as it dies, so the loss
// and the reap arrive together and the wait is a few milliseconds; a runner
// that is still running when it is declared lost (a stalled host) costs the
// whole grace, once, and is recorded as still running.
const defaultRunnerExitGrace = time.Second

// runnerExit observes ONE runner process's exit for the whole life of its
// run. The runner handle's Wait reaps the process and says why it ended;
// before this, only the standup race read it (issueStartRun), and the reason
// a runner died later — a signal, an OOM kill — was reaped and dropped, so a
// killed runner's run ended as a bare "RunnerChannel disconnected".
type runnerExit struct {
	done chan struct{}
	err  error // set before done closes
}

// watchRunner starts observing a runner process through its handle's wait;
// nil when the spawner captured no process (wait nil), which every reader
// below treats as "no exit observed".
func watchRunner(wait func() error) *runnerExit {
	if wait == nil {
		return nil
	}
	x := &runnerExit{done: make(chan struct{})}
	go func() {
		x.err = wait()
		close(x.done)
	}()
	return x
}

// exited is closed once the runner process has exited; nil (blocks forever)
// when no process is observed.
func (x *runnerExit) exited() <-chan struct{} {
	if x == nil {
		return nil
	}
	return x.done
}

// result reports the runner's exit without waiting: its Wait error (nil for
// status 0) and whether it has exited at all.
func (x *runnerExit) result() (error, bool) {
	if x == nil {
		return nil, false
	}
	select {
	case <-x.done:
		return x.err, true
	default:
		return nil, false
	}
}

// await is result, waiting up to grace for the exit.
func (x *runnerExit) await(grace time.Duration) (error, bool) {
	if x == nil {
		return nil, false
	}
	select {
	case <-x.done:
		return x.err, true
	case <-time.After(grace):
		return x.result()
	}
}

// exitStatuser is a runner Wait error that carries the process's exit status
// ALREADY CLASSIFIED — its own code, or 128+signum for a signal. The adapter
// that owns the process classifies it (exitstatus.Error, through
// exitstatus.Classify): core never reads a raw process status, so the one
// computation of it stays with the code that reaped the process.
type exitStatuser interface {
	error
	ExitStatus() int
}

// exitCodeOf is the exit status a runner's Wait error carries (exitStatuser),
// 0 for a nil error, and nil when the error carries no classified status (a
// failed reap, or a spawner that reports none).
func exitCodeOf(err error) *int32 {
	var code int32
	if err != nil {
		var es exitStatuser
		if !errors.As(err, &es) {
			return nil
		}
		code = int32(es.ExitStatus())
	}
	return &code
}

// describeRunnerExit is the runner process's exit as a run's end records it.
func describeRunnerExit(err error, exited bool) string {
	switch {
	case !exited:
		return "runner process still running when the run was ended (the coordinator ended it)"
	case err == nil:
		return "runner process exited with status 0"
	default:
		return fmt.Sprintf("runner process: %v", err)
	}
}
