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
