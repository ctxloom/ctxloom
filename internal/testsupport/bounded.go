package testsupport

import (
	"testing"
	"time"
)

// Await returns what ch delivers, failing t with format/args if nothing
// arrives within d. It exists so a wait that a defect could make endless — a
// loop that stops advancing, a gate that never opens — fails the test that
// reached it instead of parking the test binary until the global timeout.
//
// args are formatted only on failure, so a fmt.Stringer passed there (a
// captured stdout, say) renders what it holds at the deadline, not at the call.
func Await[T any](t testing.TB, d time.Duration, ch <-chan T, format string, args ...any) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatalf(format, args...)
		var zero T
		return zero
	}
}

// Within runs f on its own goroutine and returns its result, failing t with
// format/args if f has not returned within d. On failure the goroutine is
// abandoned, still running; the test has already failed.
func Within[T any](t testing.TB, d time.Duration, f func() T, format string, args ...any) T {
	t.Helper()
	got := make(chan T, 1)
	go func() { got <- f() }()
	return Await(t, d, got, format, args...)
}

// waitBudget is the longest one wait may take. It is the wait's own, not a
// share of the test binary's deadline: every test in a binary draws on that
// one deadline in turn, so a wait bounded only by it leaves the tests after a
// hang nothing, and they fail at once, blamed for the hang they followed. The
// budget is minutes because the waits it bounds are for bytes crossing a
// real pty or a call a real program makes, which a loaded machine delays, and
// a budget short enough to matter fails on an event that was merely late.
const waitBudget = 2 * time.Minute

// deadlineMargin is how far ahead of the binary's deadline a wait gives up,
// so its failure, naming what never came, is reported before the binary's
// timeout panics.
const deadlineMargin = 10 * time.Second

// Expiry fires when a wait starting now has run out: after waitBudget, or
// deadlineMargin before t's deadline if that comes first.
func Expiry(t interface{ Deadline() (time.Time, bool) }) <-chan time.Time {
	deadline, ok := t.Deadline()
	return time.After(waitBudgetAt(time.Now(), deadline, ok))
}

// waitBudgetAt is Expiry's duration for a wait starting at now.
func waitBudgetAt(now, deadline time.Time, ok bool) time.Duration {
	if !ok {
		return waitBudget
	}
	return min(waitBudget, deadline.Sub(now)-deadlineMargin)
}
