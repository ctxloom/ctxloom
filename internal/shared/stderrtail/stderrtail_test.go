package stderrtail

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Write must never fail and never short-write: this sink is attached to a dying
// child's stderr, and back-pressure there is the opposite of what it is for.
func TestRing_WriteNeverFailsAndReportsFullLength(t *testing.T) {
	r := New(8)
	n, err := r.Write([]byte("abcdefghij"))
	require.NoError(t, err)
	assert.Equal(t, 10, n, "the full input length is reported even when bytes are evicted")
}

// Bounded, drop-oldest: a dying process says why at the END, so the LAST bytes
// are what must survive.
func TestRing_KeepsTheLastBytesWithinBudget(t *testing.T) {
	r := New(8)
	_, _ = r.Write([]byte("abcdef"))
	_, _ = r.Write([]byte("ghij"))
	assert.Equal(t, "cdefghij", r.Tail())
}

// Reading the tail is NON-destructive and repeatable. A reaper may read it more
// than once (a warning, then a mailbox message), and the second read must not
// come back empty.
func TestRing_TailIsRepeatableAndNonDestructive(t *testing.T) {
	r := New(64)
	_, _ = r.Write([]byte("SyntaxError: Unexpected token 'with'\n"))
	first := r.Tail()
	require.NotEmpty(t, first)
	assert.Equal(t, first, r.Tail(), "reading the tail must not consume it")
}

// The tail is trimmed, so a child that only emitted a newline reads as silent
// rather than as a one-character diagnostic.
func TestRing_TailIsTrimmed(t *testing.T) {
	r := New(64)
	_, _ = r.Write([]byte("\n  boom  \n"))
	assert.Equal(t, "boom", r.Tail())

	empty := New(64)
	assert.Empty(t, empty.Tail(), "a child that said nothing has an empty tail")
}

// max <= 0 falls back to the standard budget rather than producing a ring that
// retains nothing — a zero-capacity diagnostic sink is a silent no-op, which is
// exactly the failure mode this package exists to prevent.
func TestNew_NonPositiveMaxUsesTheDefaultBudget(t *testing.T) {
	r := New(0)
	_, _ = r.Write([]byte(strings.Repeat("x", DefaultBytes+100)))
	assert.Len(t, r.Tail(), DefaultBytes)

	neg := New(-1)
	_, _ = neg.Write([]byte("still recorded"))
	assert.Equal(t, "still recorded", neg.Tail())
}

// Tail on a nil Ring answers "nothing" instead of panicking: the reaper path
// runs while a launch is failing, when the ring may never have been built.
func TestRing_NilReceiverTailIsEmpty(t *testing.T) {
	var r *Ring
	assert.Empty(t, r.Tail())
}

// Concurrency-safety is part of the contract, not an accident: the child's
// stderr pump writes while a reaper reads Tail. Run this under -race.
func TestRing_ConcurrentWritesAndReads(t *testing.T) {
	r := New(256)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = r.Write([]byte("noise "))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			_ = r.Tail()
		}
	}()
	wg.Wait()
	assert.NotEmpty(t, r.Tail())
}

// Capture is ADDITIVE: TeeStderr returns a writer that feeds the ring, so
// capturing a diagnostic never takes away the passthrough a caller may already
// depend on.
func TestTeeStderr_RingCapturesWhatIsWritten(t *testing.T) {
	r, w := TeeStderr(64)
	require.NotNil(t, r)
	require.NotNil(t, w)
	_, err := w.Write([]byte("captured"))
	require.NoError(t, err)
	assert.Equal(t, "captured", r.Tail())
}
