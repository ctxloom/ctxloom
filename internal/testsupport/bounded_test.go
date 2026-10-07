package testsupport

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestWithin_ReturnsWhatFReturns(t *testing.T) {
	rec := &recordingTB{}
	assert.Equal(t, 42, Within(rec, time.Second, func() int { return 42 }, "unused"))
	assert.Empty(t, rec.fatal, "a call that returns in time is not a failure")
}

func TestWithin_FailsACallThatDoesNotReturn(t *testing.T) {
	rec := &recordingTB{}
	never := make(chan struct{})
	t.Cleanup(func() { close(never) })

	got := Within(rec, time.Millisecond, func() int { <-never; return 1 }, "f(%d) did not return", 7)

	assert.Zero(t, got)
	assert.Equal(t, "f(7) did not return", rec.fatal)
}

// renders counts how often it is formatted, standing in for a live buffer
// whose contents only mean something at the moment of failure.
type renders struct{ n int }

func (r *renders) String() string { r.n++; return "state" }

func TestAwait_FormatsItsArgsOnlyOnFailure(t *testing.T) {
	rec := &recordingTB{}
	arg := &renders{}
	ready := make(chan int, 1)
	ready <- 3

	assert.Equal(t, 3, Await(rec, time.Second, ready, "unused %s", arg))
	assert.Zero(t, arg.n, "a wait that succeeds never renders its message")

	Await(rec, time.Millisecond, make(chan int), "stdout: %s", arg)
	assert.Equal(t, "stdout: state", rec.fatal)
	assert.Equal(t, 1, arg.n)
}

// TestBudgetUntil_EndsAMarginBeforeTheDeadline: a wait ends deadlineMargin
// ahead of the binary's deadline, so a failure still has room to print what it
// waited on, and it spends the whole of what is left: no limit of its own.
func TestBudgetUntil_EndsAMarginBeforeTheDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	before := time.Until(deadline)
	got := BudgetUntil(deadline, true)
	after := time.Until(deadline)
	assert.LessOrEqual(t, got, before-deadlineMargin)
	assert.GreaterOrEqual(t, got, after-deadlineMargin, "a distant deadline is the wait's to spend")
}

// TestBudgetUntil_WithoutADeadlineOutlastsAnyRun: no deadline means no bound
// worth the name, and the stand-in survives conversion to nanoseconds from
// milliseconds (a poll(2) timeout's unit) without overflowing.
func TestBudgetUntil_WithoutADeadlineOutlastsAnyRun(t *testing.T) {
	got := BudgetUntil(time.Time{}, false)
	assert.GreaterOrEqual(t, got, 24*time.Hour, "longer than any run")
	assert.LessOrEqual(t, got.Milliseconds(), int64(math.MaxInt64/int64(time.Millisecond)), "no overflow back to nanoseconds")
}
