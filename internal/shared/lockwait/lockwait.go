// Package lockwait makes a blocking lock acquisition that runs long VISIBLE.
// It holds no lock of its own and knows nothing about flock, paths, or any
// other locking mechanism — it is a generic "this call is taking a while"
// watchdog that a caller wraps around whatever blocking call it is about to
// make.
//
// It exists because acquiring an advisory file lock (safefs.Locks' Lock and
// RLock) is unconditionally blocking: a holder that never
// releases parks the caller forever — `taskloom status` reaches this on its
// exclusive write path and simply never returns. Whether that wait should
// instead FAIL is a per-call-site policy question this package does not
// answer; every caller today keeps the wait unbounded. What the wait no
// longer is, wrapped in Watch, is silent — the difference between an
// unexplained hang and one an operator can diagnose and end.
package lockwait

import (
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// After is how long an acquisition may block before it is reported as still
// waiting. Long enough that ordinary contention — one writer appending a
// line — never prints, short enough that a human who has just run a command
// and is watching it sit there gets the notice while still watching.
const After = 3 * time.Second

// LogWaitExceeded is the structured-log message a wait past After leaves
// behind. Exported so a test asserts on the record, not on scraped text.
const LogWaitExceeded = "lock_wait_exceeded"

// waitNotice is the warning a wait past After reports. The label is part
// of it: "something is stuck" is not actionable, "stuck on THIS thing" names
// what to look for.
func waitNotice(label string) string {
	return "still waiting for lock on " + label
}

// Watch starts a watchdog goroutine that reports the wait if the operation
// it guards has not finished within After: "still waiting for lock on
// <label>" as a clidiag warning for whoever is watching the terminal (its
// sink: stderr, or the session's diagnostics log under the split view), and a
// LogWaitExceeded record in the structured log for whoever reads the stall
// after the process is gone. Call the returned stop function when the
// operation completes; it
// blocks until the watchdog goroutine has settled, so no notice can print
// after Watch's caller has already returned.
//
// Only the watchdog goroutine reports; the caller stays parked in whatever
// blocking call it is making, so nothing about that call itself changes.
func Watch(label string) (stop func()) {
	settled := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		timer := time.NewTimer(After)
		defer timer.Stop()
		select {
		case <-settled:
		case <-timer.C:
			clidiag.Warn(diagProg(), "%s", waitNotice(label))
			zap.L().Warn(LogWaitExceeded, zap.String("path", label))
		}
	}()
	return func() {
		close(settled)
		<-finished
	}
}

// diagProg is the program name the wait line is stamped with. It is read from
// the running executable because this package is shared: the same wait is
// reported under taskloom, ltk and ctxloom, and each must name itself.
func diagProg() string { return progOf(os.Args[0]) }

// progOf is the program name arg0 runs as, "ctxloom" when it names none.
func progOf(arg0 string) string {
	if prog := filepath.Base(arg0); prog != "." && prog != string(filepath.Separator) {
		return prog
	}
	return "ctxloom"
}
