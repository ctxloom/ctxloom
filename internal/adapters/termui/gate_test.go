package termui

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutputGate_PassthroughWhenOpen(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)
	_, _ = g.Write([]byte("engine says hi"))
	assert.Equal(t, "engine says hi", tty.String())
}

func TestOutputGate_HoldDivertsAndReleaseReplays(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)

	_, _ = g.Write([]byte("before|"))
	g.Hold(64)
	_, _ = g.Write([]byte("held-1|"))
	_, _ = g.Write([]byte("held-2"))
	assert.Equal(t, "before|", tty.String(), "held bytes must not reach the tty")

	require.NoError(t, release(g, holdReplay, "<restore>"))
	assert.Equal(t, "before|<restore>held-1|held-2", tty.String(),
		"release writes the restore sequence, then the held bytes in order")

	_, _ = g.Write([]byte("|after"))
	assert.Equal(t, "before|<restore>held-1|held-2|after", tty.String())
}

// release is Release with one restore sequence for both modes, returning
// only the error (the mode is asserted where it matters).
func release(g *outputGate, mode holdMode, pre string) error {
	_, err := g.Release(mode, restore{replay: []byte(pre), clear: []byte(pre)})
	return err
}

var testRestore = restore{replay: []byte("<replay>"), clear: []byte("<clear>"), resume: []byte("<resume>")}

// TestOutputGate_OverflowNeverReplaysATornTail pins that an overflowed hold is
// DISCARDED, not replayed from wherever eviction left it: the old drop-oldest
// ring handed back a tail starting at an arbitrary byte, which could be the
// middle of a CSI (here: the eviction point falls inside "\x1b[31m"), and the
// guard — at ground — printed the fragment as text.
func TestOutputGate_OverflowNeverReplaysATornTail(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)

	g.Hold(8)
	_, _ = g.Write([]byte("abcde\x1b[31"))
	_, _ = g.Write([]byte("mRED"))
	used, err := g.Release(holdReplay, testRestore)
	require.NoError(t, err)

	out := tty.String()
	assert.Equal(t, holdDiscardRedraw, used, "an overflowed hold is redrawn whatever the caller preferred")
	assert.NotContains(t, out, "<replay>")
	assert.NotContains(t, out, "RED", "no held byte is replayed")
	assert.NotContains(t, out, "31m", "no torn sequence tail reaches the screen")
	assert.True(t, strings.HasPrefix(out, "<clear>"), "the screen is cleared first: %q", out)
	assert.True(t, strings.HasSuffix(out, "<resume>"), "and the region, bar and cursor come back last: %q", out)
	assert.Contains(t, out, "ctxloom: engine output while the overlay was open exceeded",
		"the loss is said on the screen, between the clear and the resume")
}

func TestOutputGate_DiscardRedrawWithoutOverflowDropsTheHoldQuietly(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)

	g.Hold(64)
	_, _ = g.Write([]byte("held"))
	used, err := g.Release(holdDiscardRedraw, testRestore)
	require.NoError(t, err)
	assert.Equal(t, holdDiscardRedraw, used)
	assert.Equal(t, "<clear><resume>", tty.String(), "the held bytes are dropped, with no overflow notice")
}

func TestOutputGate_ReplayWritesReplayThenHeldBytes(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)

	g.Hold(64)
	_, _ = g.Write([]byte("held"))
	used, err := g.Release(holdReplay, testRestore)
	require.NoError(t, err)
	assert.Equal(t, holdReplay, used)
	assert.Equal(t, "<replay>held", tty.String())
}

// TestOutputGate_HoldGrowsLazily pins that a large hold capacity (the modal's
// 8 MiB) is a bound, not an allocation made at every engagement.
func TestOutputGate_HoldGrowsLazily(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)
	g.Hold(8 << 20)
	_, _ = g.Write([]byte("tiny"))
	assert.Less(t, cap(g.hold), 1<<16, "the buffer grows with what is held")
}

// TestOutputGate_DiscardAbandonsTheGuardsUnwrittenSequence pins that a
// discard also drops the guard's held-back partial sequence: its
// continuation was in the discarded bytes, so keeping it would splice the
// engine's next write onto the front of a sequence that no longer exists.
func TestOutputGate_DiscardAbandonsTheGuardsUnwrittenSequence(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, newVTGuard(nil, nil, nil), nil)

	_, _ = g.Write([]byte("x\x1b[3")) // held back inside the guard, never written
	g.Hold(64)
	_, _ = g.Write([]byte("1m"))
	_, err := g.Release(holdDiscardRedraw, restore{})
	require.NoError(t, err)
	_, _ = g.Write([]byte("text"))
	assert.Equal(t, "xtext", tty.String(), "the engine's next write starts at ground")
}

// TestOutputGate_ReleaseWhenOpenStillWritesPre pins that Release used to
// silently discard the caller's `pre` (the full screen-restore preamble:
// panel clear, region re-assert, bar repaint, DECRC) whenever the gate was
// not held. No production caller hits this today, but nothing prevented it,
// and the failure mode was an un-restored terminal with no diagnostic. `pre`
// must be written regardless of held state; only the ring replay is
// conditional on having been held.
func TestOutputGate_ReleaseWhenOpenStillWritesPre(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)
	require.NoError(t, release(g, holdReplay, "<restore>"))
	assert.Equal(t, "<restore>", tty.String(), "pre must be written even when the gate was never held")
}

func TestOutputGate_ReleaseWhenOpenWithNoPreWritesNothing(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)
	require.NoError(t, release(g, holdReplay, ""))
	assert.Empty(t, tty.String(), "releasing an open gate with no pre writes nothing")
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

// TestOutputGate_Release_ReturnsWriteErrors pins that Release used to
// discard the error from all three dst.Write calls (the restore sequence,
// the replayed held bytes, and the truncation notice), so a failing tty
// silently lost the entire replay — the ring is drained unconditionally, so
// those bytes exist nowhere else once Release returns. Release must now
// return that failure instead of swallowing it.
func TestOutputGate_Release_ReturnsWriteErrors(t *testing.T) {
	var mu sync.Mutex
	boom := errors.New("boom: tty gone")
	fw := &failWriter{okCount: 0, err: boom} // every dst.Write from here fails
	g := newOutputGate(&mu, fw, nil, nil)

	g.Hold(64)
	_, _ = g.Write([]byte("held bytes"))

	err := release(g, holdReplay, "<restore>")
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
	g := newOutputGate(&mu, fw, nil, nil)

	g.Hold(8)
	_, _ = g.Write([]byte("0123456789abcdef")) // overflow: exercises the notice write

	_, err := g.Release(holdReplay, testRestore)
	require.Error(t, err)
	// clear, notice and resume are separate dst.Write calls; failWriter
	// fails all of them, so every one must still have been attempted.
	assert.GreaterOrEqual(t, fw.calls, 3, "clear + notice + resume must all still be attempted despite the first failing")
}

// TestOutputGate_Release_CallsAfterWrite pins that Release never called
// g.afterWrite, so a bar marked dirty by the replayed data itself (the guard
// filtering the drained ring can call barDamaged, e.g. the engine emitted
// ED 2 while the viewer was open) was never flushed — a blank bar on the
// normal success path.
func TestOutputGate_Release_CallsAfterWrite(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	calls := 0
	g := newOutputGate(&mu, &tty, nil, func() { calls++ })

	g.Hold(64)
	_, _ = g.Write([]byte("held"))
	require.NoError(t, release(g, holdReplay, "<restore>"))
	assert.Equal(t, 1, calls, "Release must run afterWrite exactly as Write does")
}

// TestOutputGate_FlushGuard_WritesPendingBytes pins that bytes the guard
// holds back (a pending incomplete escape/CSI sequence, or a split UTF-8
// rune tail) had no flush path at all — Close called gate.Release(nil),
// which never asked the guard to flush, so a session ending mid-sequence
// silently dropped those bytes. FlushGuard is the teardown-only path that
// commits them: it must NOT be folded into ordinary Release, since a
// mid-engagement release (the engine keeps running afterward) would tear a
// sequence the engine's own next write was going to complete.
func TestOutputGate_FlushGuard_WritesPendingBytes(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	guard := newVTGuard(nil, nil, nil)
	g := newOutputGate(&mu, &tty, guard, nil)

	_, _ = g.Write([]byte("x\x1b")) // trailing incomplete escape: held inside the guard
	assert.Equal(t, "x", tty.String(), "an incomplete escape must not reach the tty yet")

	require.NoError(t, g.FlushGuard())
	assert.Equal(t, "x\x1b", tty.String(), "teardown must flush the pending escape byte rather than drop it")
}

func TestOutputGate_FlushGuard_NoopWithoutGuard(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	g := newOutputGate(&mu, &tty, nil, nil)
	require.NoError(t, g.FlushGuard())
	assert.Empty(t, tty.String())
}

func TestOutputGate_FlushGuard_NoopWhenNothingPending(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	guard := newVTGuard(nil, nil, nil)
	g := newOutputGate(&mu, &tty, guard, nil)
	_, _ = g.Write([]byte("plain text"))
	require.NoError(t, g.FlushGuard())
	assert.Equal(t, "plain text", tty.String())
}

func TestOutputGate_AfterWriteHookRidesPassthroughOnly(t *testing.T) {
	var mu sync.Mutex
	var tty bytes.Buffer
	calls := 0
	g := newOutputGate(&mu, &tty, nil, func() { calls++ })

	_, _ = g.Write([]byte("a"))
	assert.Equal(t, 1, calls)
	g.Hold(64)
	_, _ = g.Write([]byte("b"))
	assert.Equal(t, 1, calls, "held writes never trigger the bar flush hook")
}
