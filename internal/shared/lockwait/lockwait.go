// Package lockwait makes a blocking lock acquisition that runs long VISIBLE.
// It holds no lock of its own and knows nothing about flock, paths, or any
// other locking mechanism — it is a generic "this call is taking a while"
// watchdog that a caller wraps around whatever blocking call it is about to
// make.
//
// It exists because acquiring an advisory file lock (github.com/gofrs/flock's
// Flock.Lock / Flock.RLock) is unconditionally blocking: a holder that never
// releases parks the caller forever — `taskloom status` reaches this on its
// exclusive write path and simply never returns. Whether that wait should
// instead FAIL is a per-call-site policy question this package does not
// answer; every caller today keeps the wait unbounded. What the wait no
// longer is, wrapped in Watch, is silent — the difference between an
// unexplained hang and one an operator can diagnose and end.
package lockwait

import (
	"fmt"
	"os"
	"time"
)

// After is how long an acquisition may block before it is reported as still
// waiting. Long enough that ordinary contention — one writer appending a
// line — never prints, short enough that a human who has just run a command
// and is watching it sit there gets the notice while still watching.
const After = 3 * time.Second

// Watch starts a watchdog goroutine that writes "still waiting for lock on
// <label>" to stderr if the operation it guards has not finished within
// After. Call the returned stop function when the operation completes; it
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
			fmt.Fprintf(os.Stderr, "still waiting for lock on %s\n", label)
		}
	}()
	return func() {
		close(settled)
		<-finished
	}
}
