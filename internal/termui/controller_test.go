package termui

import (
	"bytes"
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
	assert.Equal(t, 24, geo.Rows)
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
	clearAt := strings.LastIndex(out, "\x1b[17;1H\x1b[J") // rows−panel+1 = 24−8+1
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

	// Interceptor is back to passthrough.
	_, _ = h.stdinW.Write([]byte("typed-after"))
	waitFor(t, "passthrough restored", func() bool { return h.engine.String() == "typed-after" })
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

func TestController_RosterPollFeedsBar(t *testing.T) {
	const interval = 5 * time.Millisecond
	var fetches atomic.Int64
	var closing atomic.Bool
	h := newCtlHarness(t, func(o *Options) {
		o.RosterInterval = interval
		o.FetchRoster = func() ([]RosterEntry, error) {
			fetches.Add(1)
			if closing.Load() {
				// Straggler poll after Close: error out so pollRoster skips
				// SetRoster→RequestPaint and never reads the nowNanos seam
				// again (see the drain note below).
				return nil, fmt.Errorf("test: fetch after close")
			}
			return []RosterEntry{{Harp: "swift-elm-fox", State: "executing", LastActivityUnix: 1}}, nil
		}
	})
	h.src <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "roster digest on the bar", func() bool {
		return strings.Contains(h.tty.String(), "swift-elm-fox→executing")
	})
	closing.Store(true)
	h.c.Close()
	// Close signals pollRoster (close(stopRoster)) but does not JOIN its
	// goroutine, whose select between an already-fired ticker and the close
	// has no ordering guarantee. Left alone, that goroutine outlives this
	// test and reaches RequestPaint's read of the package-level nowNanos
	// seam concurrently with a later test's unsynchronized swap of it
	// (surround_test.go / tear_test.go / incident_repro_test.go) — and the
	// race detector attributes that leaked-goroutine race to whichever test
	// is running when it pairs the accesses: observed once under full-suite
	// load as a spurious failure of the deterministic, unrelated
	// TestVTGuard_ChildDECSCClearedBySoftResetAndAltLeave. Two layers close
	// it down (deadline-poll pattern, not a blind sleep): the closing flag
	// above makes any post-Close fetch inert (error → no seam read), and
	// this drain waits out a full ticker-cadence window with no new fetch —
	// meaning the poller has exited, or at worst will next observe the
	// closed stopRoster / the closing flag without ever touching the seam.
	waitFor(t, "roster poll goroutine drained", func() bool {
		before := fetches.Load()
		time.Sleep(4 * interval)
		return fetches.Load() == before
	})
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
