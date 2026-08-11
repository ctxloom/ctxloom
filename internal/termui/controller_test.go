package termui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a goroutine-safe tty stand-in.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// fakeOverlay records its lifecycle; Run blocks until released (or aborted).
type fakeOverlay struct {
	started chan OverlayGeometry
	release chan error
	ui      lockedBuffer
	aborts  chan struct{}
}

func newFakeOverlay() *fakeOverlay {
	return &fakeOverlay{
		started: make(chan OverlayGeometry, 1),
		release: make(chan error, 1),
		aborts:  make(chan struct{}, 4),
	}
}

func (f *fakeOverlay) Run(in io.Reader, _ io.Writer, geo OverlayGeometry) error {
	f.started <- geo
	go func() { _, _ = io.Copy(&f.ui, in) }()
	return <-f.release
}

func (f *fakeOverlay) Abort() {
	f.aborts <- struct{}{}
	select {
	case f.release <- nil:
	default:
	}
}

// ctlHarness assembles a controller over pipe-backed stdin, a locked tty, and
// a test resize source, with a pump goroutine standing in for the plugin
// client's stdin pump.
type ctlHarness struct {
	c       *Controller
	stdinW  *io.PipeWriter
	tty     *lockedBuffer
	src     chan *pb.WindowSize
	engine  lockedBuffer // what the engine "receives" from the pump
	warns   chan string
	overlay *fakeOverlay
	pumpEnd chan struct{}
}

func newCtlHarness(t *testing.T, mutate func(*Options)) *ctlHarness {
	t.Helper()
	pr, pw := io.Pipe()
	h := &ctlHarness{
		stdinW:  pw,
		tty:     &lockedBuffer{},
		src:     make(chan *pb.WindowSize, 4),
		warns:   make(chan string, 4),
		overlay: newFakeOverlay(),
		pumpEnd: make(chan struct{}),
	}
	opts := Options{
		Stdin:      pr,
		TTY:        h.tty,
		Resize:     h.src,
		Prefix:     testPrefix,
		Surround:   true,
		Bar:        BarInfo{Harp: "perky-same-chevy", Engine: "claude-code", PrefixHint: "^]"},
		NewOverlay: func() Overlay { return h.overlay },
		Warn:       func(format string, args ...any) { h.warns <- fmt.Sprintf(format, args...) },
	}
	if mutate != nil {
		mutate(&opts)
	}
	h.c = New(opts)
	go func() {
		defer close(h.pumpEnd)
		buf := make([]byte, 4096)
		for {
			n, err := h.c.Stdin().Read(buf)
			if n > 0 {
				_, _ = h.engine.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		// Close drains any in-flight overlay goroutine (engage → runOverlay)
		// by waiting on sessionMu; without it that goroutine can outlive the
		// test and read the nowNanos seam concurrently with the next test's
		// swap of it (a cross-test data race). tearHarness already Closes here.
		h.c.Close()
		_ = pw.Close()
		close(h.src)
		<-h.pumpEnd
	})
	return h
}

// waitFor polls until cond holds (hermetic replacement for sleeps).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *ctlHarness) drainTranslated(t *testing.T) *pb.WindowSize {
	return recvSize(t, h.c.Resize())
}

func TestController_EngageHoldReplayNudge(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	ws := h.drainTranslated(t)
	require.Equal(t, uint32(23), ws.Rows, "initial size reaches the engine reserved")
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	// Engage: prefix + a viewer key.
	_, err := h.stdinW.Write([]byte{testPrefix, 'j'})
	require.NoError(t, err)
	var geo OverlayGeometry
	select {
	case geo = <-h.overlay.started:
	case <-time.After(2 * time.Second):
		t.Fatal("overlay never started")
	}
	assert.Equal(t, 23, geo.Rows, "geo.Rows is the DRAWABLE height (real 24 rows minus the surround's 1-row reservation)")
	assert.Equal(t, 80, geo.Cols)
	assert.Equal(t, 8, geo.PanelRows, "bottom third floored at 8 rows")
	assert.Contains(t, h.tty.String(), "\x1b7\x1b[r",
		"engage saves the engine cursor and hands the overlay the full scroll region")
	waitFor(t, "viewer key routed", func() bool { return h.overlay.ui.String() == "j" })

	// Engine output during engagement is held.
	before := h.tty.String()
	_, _ = h.c.Stdout().Write([]byte("HELD-OUTPUT"))
	assert.NotContains(t, h.tty.String(), "HELD-OUTPUT")
	assert.Equal(t, before, h.tty.String(), "nothing hits the tty while held")

	// Disengage: one atomic restore — panel cleared, scroll region + bar
	// re-established, engine cursor restored — then the replay, then a nudge.
	h.overlay.release <- nil
	waitFor(t, "held output replayed", func() bool { return strings.Contains(h.tty.String(), "HELD-OUTPUT") })
	out := h.tty.String()
	clearAt := strings.LastIndex(out, "\x1b[16;1H\x1b[J") // drawable−panel+1 = (24−1)−8+1
	regionAt := strings.LastIndex(out, "\x1b[1;23r")
	cursorAt := strings.LastIndex(out, "\x1b8")
	replayAt := strings.Index(out, "HELD-OUTPUT")
	require.GreaterOrEqual(t, clearAt, 0, "panel region cleared")
	assert.Less(t, clearAt, regionAt, "region re-established after the clear")
	assert.Less(t, regionAt, cursorAt, "engine cursor restored after the bar repaint")
	assert.Less(t, cursorAt, replayAt, "the replay lands on a fully restored screen")

	first := h.drainTranslated(t)
	second := h.drainTranslated(t)
	assert.Equal(t, uint32(22), first.Rows, "repaint nudge wiggles a row")
	assert.Equal(t, uint32(23), second.Rows)

	// interceptor is back to passthrough.
	_, _ = h.stdinW.Write([]byte("typed-after"))
	waitFor(t, "passthrough restored", func() bool { return h.engine.String() == "typed-after" })
}

// TestController_EngageGeometryExcludesReservedRow is DEFECT D: the overlay
// used to be handed the FULL terminal height (geo.Rows == the real terminal
// rows), so its last content row (geo.Rows−geo.PanelRows+1 .. geo.Rows)
// landed exactly on the surround's reserved bar row (real row `rows`). The
// overlay must instead be handed the DRAWABLE rows — the same
// reservation-subtracted height the engine's own viewport gets
// (resizeTranslator.Translate) — so neither the quick panel nor a
// full-screen presentation (whose totalHeight is geo.Rows) can ever address
// that row.
func TestController_EngageGeometryExcludesReservedRow(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	_, err := h.stdinW.Write([]byte{testPrefix, 'j'})
	require.NoError(t, err)
	var geo OverlayGeometry
	select {
	case geo = <-h.overlay.started:
	case <-time.After(2 * time.Second):
		t.Fatal("overlay never started")
	}

	const realRows = 24
	assert.Equal(t, realRows-surroundReserve, geo.Rows,
		"the overlay's Rows is the DRAWABLE height (real rows minus the surround's reservation)")

	lastContentRow := geo.Rows - geo.PanelRows + 1 + geo.PanelRows - 1
	assert.LessOrEqual(t, lastContentRow, realRows-surroundReserve,
		"the overlay's last content row must never reach the reserved bar row (real row %d)", realRows)
	assert.Equal(t, geo.Rows, lastContentRow, "sanity: last content row is exactly geo.Rows")
}

// TestController_ResizeWhileEngagedDoesNotRepaintBar is the suspend-guard
// defect: surround.SetSize did not check s.suspended, so a SIGWINCH arriving
// while the overlay owns the screen repainted the bar (and re-established
// DECSTBM) directly on top of the live overlay. While suspended, a resize
// must update recorded size state (so ResumeSequence and the translator pick
// up the new dimensions) without writing anything to the tty; the repaint is
// deferred to ResumeSequence on release.
func TestController_ResizeWhileEngagedDoesNotRepaintBar(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	_, err := h.stdinW.Write([]byte{testPrefix, 'j'})
	require.NoError(t, err)
	select {
	case <-h.overlay.started:
	case <-time.After(2 * time.Second):
		t.Fatal("overlay never started")
	}

	before := h.tty.String()
	h.src <- &pb.WindowSize{Rows: 30, Cols: 100} // SIGWINCH while engaged
	// The resize's translated size still reaches the (held) engine viewport —
	// that path is unaffected by suspension — but nothing may land on the
	// live tty: no DECSTBM re-assert, no bar repaint.
	ws := h.drainTranslated(t)
	assert.Equal(t, uint32(29), ws.Rows, "the translated size still reflects the new dimensions")
	assert.Equal(t, before, h.tty.String(), "a resize while the overlay owns the screen must not paint the tty")

	// Release: the NEW size (recorded during suspension) must be what
	// ResumeSequence re-establishes, proving the resize wasn't just dropped.
	h.overlay.release <- nil
	waitFor(t, "resume with the new size", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;29r") })
}

func TestController_DoublePressLiteralAbortsOverlay(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)

	// Two writes → two read chunks → engage fires, then the literal aborts it.
	_, _ = h.stdinW.Write([]byte{testPrefix})
	select {
	case <-h.overlay.started:
	case <-time.After(2 * time.Second):
		t.Fatal("overlay never started")
	}
	_, _ = h.stdinW.Write([]byte{testPrefix})
	waitFor(t, "literal prefix reaches the engine", func() bool { return h.engine.String() == string(testPrefix) })
	select {
	case <-h.overlay.aborts:
	case <-time.After(2 * time.Second):
		t.Fatal("overlay was never aborted")
	}
}

func TestController_OverlayErrorDegradesToPlainTerminal(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)

	_, _ = h.stdinW.Write([]byte{testPrefix, 'j'})
	<-h.overlay.started
	h.overlay.release <- fmt.Errorf("boom")

	select {
	case w := <-h.warns:
		assert.Contains(t, w, "plain terminal")
		assert.Contains(t, w, "boom")
	case <-time.After(2 * time.Second):
		t.Fatal("no degradation warning streamed")
	}

	// The prefix now passes through — the session must survive the UI.
	_, _ = h.stdinW.Write([]byte{testPrefix, 'x'})
	waitFor(t, "degraded passthrough", func() bool {
		return h.engine.String() == string(testPrefix)+"x"
	})
}

func TestController_FactoryPanicDegrades(t *testing.T) {
	h := newCtlHarness(t, func(o *Options) {
		o.NewOverlay = func() Overlay { panic("factory exploded") }
	})
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)

	_, _ = h.stdinW.Write([]byte{testPrefix, 'j'})
	select {
	case w := <-h.warns:
		assert.Contains(t, w, "factory exploded")
	case <-time.After(2 * time.Second):
		t.Fatal("no warning for the factory panic")
	}
	_, _ = h.stdinW.Write([]byte("still-typing"))
	waitFor(t, "engine keeps receiving input", func() bool {
		return strings.Contains(h.engine.String(), "still-typing")
	})
}

func TestController_CloseRestoresTerminal(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	h.c.Close()
	out := h.tty.String()
	assert.Contains(t, out, "\x1b[r", "full scroll region restored on exit")
	assert.True(t, strings.HasSuffix(out, "\x1b[24;1H\x1b[2K"), "bar row cleared last")

	h.c.Close() // idempotent
}

func TestController_CloseWhileEngagedFlushesHeldOutput(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)

	_, _ = h.stdinW.Write([]byte{testPrefix, 'j'})
	<-h.overlay.started
	_, _ = h.c.Stdout().Write([]byte("FINAL-WORDS"))

	h.c.Close()
	assert.Contains(t, h.tty.String(), "FINAL-WORDS",
		"held engine output must not vanish when the run ends mid-engagement")
}

// TestController_ApprovalPollFeedsBarAndRingsBellOnce wires FetchApprovals
// through the SAME poller FetchRoster already runs on (no separate ticker)
// and pins the observable end-to-end effect: the bar shows the "⚠N " prefix,
// and the bell rings exactly once for the 0→N transition even though the
// poller ticks several more times at the same nonzero count.
func TestController_ApprovalPollFeedsBarAndRingsBellOnce(t *testing.T) {
	var n atomic.Int32
	h := newCtlHarness(t, func(o *Options) {
		o.RosterInterval = 5 * time.Millisecond
		o.FetchRoster = func() ([]RosterEntry, error) { return nil, nil }
		o.FetchApprovals = func() int { return int(n.Load()) }
	})
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	n.Store(2)
	waitFor(t, "approval indicator on the bar", func() bool { return strings.Contains(h.tty.String(), "⚠2") })
	// Give the poller (5ms interval) several more ticks at the SAME count —
	// only the 0→N transition may have rung.
	waitFor(t, "settles at one bell", func() bool { return strings.Contains(h.tty.String(), "⚠2") })
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, 1, strings.Count(h.tty.String(), "\a"), "bell rings exactly once for the 0→N transition")

	h.c.Close()
}

func TestController_RosterPollFeedsBar(t *testing.T) {
	h := newCtlHarness(t, func(o *Options) {
		o.RosterInterval = 5 * time.Millisecond
		o.FetchRoster = func() ([]RosterEntry, error) {
			return []RosterEntry{{Harp: "swift-elm-fox", State: "executing", LastActivityUnix: 1}}, nil
		}
	})
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "roster digest on the bar", func() bool {
		return strings.Contains(h.tty.String(), "swift-elm-fox→executing")
	})
	h.c.Close()
}

// TestController_RosterFetchWarnsAfterConsecutiveFailures pins the safe half:
// pollRoster discarded every FetchRoster error forever, with no
// counter and no eventual warning, so a permanently broken coordinator
// connection was indistinguishable from a stable roster. The last-good
// snapshot must still stay displayed (unchanged, documented intent), but
// enough consecutive failures must now surface exactly one warning, and a
// later success must reset the streak. Exercises rosterFetch directly
// (no goroutine/ticker) to stay fully deterministic — the goroutine-join
// half is a separate, tracked concern (fussy-plow/rapid-grass) and is
// deliberately untouched here.
func TestController_RosterFetchWarnsAfterConsecutiveFailures(t *testing.T) {
	boom := errors.New("coordinator unreachable")
	fetchErr := true
	var mu sync.Mutex
	var warns []string
	c := &Controller{
		opts: Options{
			FetchRoster: func() ([]RosterEntry, error) {
				if fetchErr {
					return nil, boom
				}
				return []RosterEntry{{Harp: "h", State: "executing"}}, nil
			},
			Warn: func(format string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				warns = append(warns, fmt.Sprintf(format, args...))
			},
		},
	}
	var tty bytes.Buffer
	c.sur = newTestSurround(&tty, BarInfo{Harp: "h"})

	for i := 0; i < rosterFailWarnThreshold-1; i++ {
		c.rosterFetch()
	}
	mu.Lock()
	assert.Empty(t, warns, "must stay silent below the threshold")
	mu.Unlock()

	c.rosterFetch() // the Nth consecutive failure
	mu.Lock()
	require.Len(t, warns, 1, "must warn exactly at the threshold")
	assert.Contains(t, warns[0], "coordinator unreachable")
	mu.Unlock()

	c.rosterFetch() // one more failure past the threshold
	mu.Lock()
	assert.Len(t, warns, 1, "must not spam a warning on every subsequent failure")
	mu.Unlock()

	fetchErr = false
	c.rosterFetch() // a success resets the streak
	fetchErr = true
	for i := 0; i < rosterFailWarnThreshold-1; i++ {
		c.rosterFetch()
	}
	mu.Lock()
	assert.Len(t, warns, 1, "the reset streak must not yet be back at the threshold")
	mu.Unlock()
}

func TestController_NoSizeYetRefusesEngage(t *testing.T) {
	h := newCtlHarness(t, nil)
	// No size event at all: engaging can't lay out a panel; keystrokes drop
	// back to passthrough rather than wedging.
	_, _ = h.stdinW.Write([]byte{testPrefix, 'j'})
	_, _ = h.stdinW.Write([]byte("after"))
	waitFor(t, "passthrough after refused engage", func() bool {
		return strings.Contains(h.engine.String(), "after")
	})
	select {
	case <-h.overlay.started:
		t.Fatal("overlay must not start without a known terminal size")
	default:
	}
}
