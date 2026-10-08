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

// deadlineMargin is how far ahead of the test binary's deadline BudgetUntil
// ends: room for the failing assertion to print what the wait saw before go
// test's own timeout panics over it.
const deadlineMargin = 10 * time.Second

// noDeadline stands in for a bound when there is no deadline to honour: longer
// than any run, yet short enough that a callee converting it to milliseconds
// and back to nanoseconds (a poll(2) timeout) does not overflow.
const noDeadline = 100 * 365 * 24 * time.Hour

// BudgetUntil is how long a wait on an event may take when the only bound is
// deadline (ok reports whether there is one): until deadlineMargin before it,
// or noDeadline without one. A wait for bytes crossing a real pty, a call a
// real program makes, or a real process exiting carries no limit of its own:
// a loaded machine delays those by any amount, and a limit short enough to
// matter fails on an event that was merely late.
//
// Every wait in a test binary draws on that binary's one deadline, so a wait
// that hangs leaves the tests after it an already-spent budget, and they fail
// at once, blamed for the hang they followed. What keeps that from happening
// is the invariant every caller owes: a wait ends as soon as what it waits for
// can no longer arrive, not only when it does. A wait whose condition can go
// permanently false (a screen model that has met a sequence it cannot replay,
// a process that has already exited) checks for that and fails at once.
func BudgetUntil(deadline time.Time, ok bool) time.Duration {
	if !ok {
		return noDeadline
	}
	return time.Until(deadline) - deadlineMargin
}

// Budget is BudgetUntil t's deadline, for a wait that takes a duration.
func Budget(t interface{ Deadline() (time.Time, bool) }) time.Duration {
	return BudgetUntil(t.Deadline())
}

// Expiry is Budget as a channel that fires when it runs out.
func Expiry(t interface{ Deadline() (time.Time, bool) }) <-chan time.Time {
	return time.After(Budget(t))
}
