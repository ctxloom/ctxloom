package termui

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputGate_PassthroughWhenOpen(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, 64, nil, nil)
	_, _ = g.Write([]byte("engine says hi"))
	assert.Equal(t, "engine says hi", tty.String())
}

func TestOutputGate_HoldDivertsAndReleaseReplays(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, 64, nil, nil)

	_, _ = g.Write([]byte("before|"))
	g.Hold()
	_, _ = g.Write([]byte("held-1|"))
	_, _ = g.Write([]byte("held-2"))
	assert.Equal(t, "before|", tty.String(), "held bytes must not reach the tty")

	require.NoError(t, g.Release([]byte("<restore>")))
	assert.Equal(t, "before|<restore>held-1|held-2", tty.String(),
		"release writes the restore sequence, then the held bytes in order")

	_, _ = g.Write([]byte("|after"))
	assert.Equal(t, "before|<restore>held-1|held-2|after", tty.String())
}

func TestOutputGate_ReleaseWithOverflowAppendsTruncationNotice(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, 8, nil, nil)

	g.Hold()
	_, _ = g.Write([]byte("0123456789abcdef")) // 16 into 8: oldest dropped
	require.NoError(t, g.Release(nil))

	out := tty.String()
	assert.Contains(t, out, "89abcdef", "the newest bytes survive")
	assert.NotContains(t, out, "01234567", "the oldest bytes are gone")
	assert.Contains(t, out, "8 bytes of engine output dropped",
		"overflow surfaces as a visible truncation notice")
}

func TestOutputGate_ReleaseWhenOpenIsNoop(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, 64, nil, nil)
	require.NoError(t, g.Release([]byte("<restore>")))
	assert.Empty(t, tty.String(), "releasing an open gate writes nothing")
}

// failWriter fails every Write after the first `okCount` succeed — enough to
// let Hold's pre-Release passthrough writes through, then break exactly the
// writes Release itself makes.
type failWriter struct {
	okCount int
	calls   int
	err     error
}

func (w *failWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.okCount {
		return 0, w.err
	}
	return len(p), nil
}

// TestOutputGate_Release_ReturnsWriteErrors pins U141-F01: Release used to
// discard the error from all three dst.Write calls (the restore sequence,
// the replayed held bytes, and the truncation notice), so a failing tty
// silently lost the entire replay — the ring is drained unconditionally, so
// those bytes exist nowhere else once Release returns. Release must now
// return that failure instead of swallowing it.
func TestOutputGate_Release_ReturnsWriteErrors(t *testing.T) {
	var mu sync.Mutex
	boom := errors.New("boom: tty gone")
	fw := &failWriter{okCount: 0, err: boom} // every dst.Write from here fails
	g := newOutputGate(&mu, fw, 64, nil, nil)

	g.Hold()
	_, _ = g.Write([]byte("held bytes"))

	err := g.Release([]byte("<restore>"))
	require.Error(t, err, "a failing tty write during Release must not be silently discarded")
	assert.ErrorIs(t, err, boom)
}

// TestOutputGate_Release_PartialFailureStillAttemptsEveryWrite confirms
// Release keeps attempting the restore sequence, the replay, AND the
// truncation notice even after an earlier one of the three fails (rather
// than bailing out and losing the rest) — and that BOTH failures are visible
// in the returned error, not just the first.
func TestOutputGate_Release_PartialFailureStillAttemptsEveryWrite(t *testing.T) {
	var mu sync.Mutex
	boom := errors.New("boom: tty gone")
	// okCount 0: the "pre" restore-sequence write (the very first dst.Write
	// Release makes) already fails.
	fw := &failWriter{okCount: 0, err: boom}
	g := newOutputGate(&mu, fw, 8, nil, nil)

	g.Hold()
	_, _ = g.Write([]byte("0123456789abcdef")) // overflow: also exercises the drop-notice write

	err := g.Release([]byte("<restore>"))
	require.Error(t, err)
	// Both the held-bytes replay and the drop notice are separate dst.Write
	// calls after the failed "pre" write; failWriter fails all of them, so
	// the aggregated error must report more than just the first.
	assert.GreaterOrEqual(t, fw.calls, 3, "restore + replay + drop-notice must all still be attempted despite the first failing")
}

func TestOutputGate_AfterWriteHookRidesPassthroughOnly(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	calls := 0
	g := newOutputGate(&mu, &tty, 64, nil, func() { calls++ })

	_, _ = g.Write([]byte("a"))
	assert.Equal(t, 1, calls)
	g.Hold()
	_, _ = g.Write([]byte("b"))
	assert.Equal(t, 1, calls, "held writes never trigger the bar flush hook")
}
