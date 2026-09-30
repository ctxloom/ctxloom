package termui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
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
	resizes chan OverlayGeometry
	armed   chan int
	notices chan Notice
	// frame, when set, is written to the tty as Run starts: the first frame.
	frame string
}

func newFakeOverlay() *fakeOverlay {
	return &fakeOverlay{
		started: make(chan OverlayGeometry, 1),
		release: make(chan error, 1),
		aborts:  make(chan struct{}, 4),
		resizes: make(chan OverlayGeometry, 4),
		armed:   make(chan int, 4),
		notices: make(chan Notice, 4),
	}
}

func (f *fakeOverlay) Run(in io.Reader, tty io.Writer, geo OverlayGeometry) error {
	if f.frame != "" {
		_, _ = io.WriteString(tty, f.frame)
	}
	f.started <- geo
	go func() { _, _ = io.Copy(&f.ui, in) }()
	return <-f.release
}

func (f *fakeOverlay) Resize(geo OverlayGeometry) { f.resizes <- geo }
func (f *fakeOverlay) Armed(discarded int)        { f.armed <- discarded }
func (f *fakeOverlay) Notify(n Notice)            { f.notices <- n }

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
	src     chan *agent.WindowSize
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
		src:     make(chan *agent.WindowSize, 4),
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
		NewOverlay: func(OverlayStart) Overlay { return h.overlay },
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

func (h *ctlHarness) drainTranslated(t *testing.T) *agent.WindowSize {
	return recvSize(t, h.c.Resize())
}

func TestController_EngageHoldReplayNudge(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	ws := h.drainTranslated(t)
	require.Equal(t, uint16(23), ws.Rows, "initial size reaches the engine reserved")
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
	assert.Contains(t, h.tty.String(), "\x1b[?1049h\x1b[r",
		"engage moves to the alternate screen (saving the engine's screen and cursor) with the full scroll region")
	waitFor(t, "viewer key routed", func() bool { return h.overlay.ui.String() == "j" })

	// Engine output during engagement is held.
	before := h.tty.String()
	_, _ = h.c.Stdout().Write([]byte("HELD-OUTPUT"))
	assert.NotContains(t, h.tty.String(), "HELD-OUTPUT")
	assert.Equal(t, before, h.tty.String(), "nothing hits the tty while held")

	// Disengage: one atomic restore — back to the engine's screen, scroll
	// region + bar re-established, engine cursor restored — then the replay,
	// then a nudge.
	h.overlay.release <- nil
	waitFor(t, "held output replayed", func() bool { return strings.Contains(h.tty.String(), "HELD-OUTPUT") })
	out := h.tty.String()
	leaveAt := strings.LastIndex(out, "\x1b[?1049l")
	regionAt := strings.LastIndex(out, "\x1b[1;23r")
	cursorAt := strings.LastIndex(out, "\x1b8")
	replayAt := strings.Index(out, "HELD-OUTPUT")
	require.GreaterOrEqual(t, leaveAt, 0, "release leaves the alternate screen")
	assert.NotContains(t, out, "\x1b[16;1H\x1b[J", "nothing of the engine's screen is erased: it was never drawn over")
	assert.Less(t, leaveAt, regionAt, "region re-established on the engine's screen")
	assert.Less(t, regionAt, cursorAt, "engine cursor restored after the bar repaint")
	assert.Less(t, cursorAt, replayAt, "the replay lands on a fully restored screen")

	first := h.drainTranslated(t)
	second := h.drainTranslated(t)
	assert.Equal(t, uint16(22), first.Rows, "repaint nudge wiggles a row")
	assert.Equal(t, uint16(23), second.Rows)

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
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
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
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
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
	h.src <- &agent.WindowSize{Rows: 30, Cols: 100} // SIGWINCH while engaged
	// The resize's translated size still reaches the (held) engine viewport —
	// that path is unaffected by suspension — but nothing may land on the
	// live tty: no DECSTBM re-assert, no bar repaint.
	ws := h.drainTranslated(t)
	assert.Equal(t, uint16(29), ws.Rows, "the translated size still reflects the new dimensions")
	assert.Equal(t, before, h.tty.String(), "a resize while the overlay owns the screen must not paint the tty")

	// Release: the NEW size (recorded during suspension) must be what
	// ResumeSequence re-establishes, proving the resize wasn't just dropped.
	h.overlay.release <- nil
	waitFor(t, "resume with the new size", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;29r") })
}

func TestController_DoublePressLiteralAbortsOverlay(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
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
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
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
		o.NewOverlay = func(OverlayStart) Overlay { panic("factory exploded") }
	})
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
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
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
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
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)

	_, _ = h.stdinW.Write([]byte{testPrefix, 'j'})
	<-h.overlay.started
	_, _ = h.c.Stdout().Write([]byte("FINAL-WORDS"))

	h.c.Close()
	assert.Contains(t, h.tty.String(), "FINAL-WORDS",
		"held engine output must not vanish when the run ends mid-engagement")
}

// TestController_SetApprovalsRingsPerArrivalRateLimited pins the bell
// discipline: every ARRIVAL may ring (a second child's request is not
// silent), a burst rings once per approvalBellInterval, a count change that
// is not an arrival never rings, and the bar carries the count.
func TestController_SetApprovalsRingsPerArrivalRateLimited(t *testing.T) {
	clk := fakeclock.New()
	h := newCtlHarness(t, func(o *Options) { o.Clock = clk })
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })
	bells := func() int { return strings.Count(h.tty.String(), "\a") }

	h.c.SetApprovals(1, clk.Now().Add(-65*time.Second), true)
	assert.Equal(t, 1, bells(), "an arrival rings")
	assert.Contains(t, h.tty.String(), "⚑ 1 · oldest 01:05", "the bar carries the count and the oldest age, on the controller's clock")

	h.c.SetApprovals(2, clk.Now(), true)
	assert.Equal(t, 1, bells(), "a second arrival inside the interval is folded into the first bell")

	clk.Advance(approvalBellInterval)
	h.c.SetApprovals(3, clk.Now(), true)
	assert.Equal(t, 2, bells(), "an arrival after the interval rings again — even at a nonzero count")

	clk.Advance(approvalBellInterval)
	h.c.SetApprovals(2, clk.Now(), false)
	assert.Equal(t, 2, bells(), "a resolution is not an arrival")
}

// TestController_SuppressedBellDoesNotSpendTheInterval pins that a bell the
// bar could not ring (suspended under an overlay) does not count against the
// rate limit: the next arrival on a visible bar still rings.
func TestController_SuppressedBellDoesNotSpendTheInterval(t *testing.T) {
	clk := fakeclock.New()
	h := newCtlHarness(t, func(o *Options) { o.Clock = clk })
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	h.c.sur.Suspend()
	h.c.SetApprovals(1, clk.Now(), true)
	assert.NotContains(t, h.tty.String(), "\a", "no bell while the bar is suspended")
	_ = h.c.sur.ResumeSequence()
	h.c.SetApprovals(2, clk.Now(), true)
	assert.Contains(t, h.tty.String(), "\a")
}

func TestController_RosterPollFeedsBar(t *testing.T) {
	h := newCtlHarness(t, func(o *Options) {
		o.RosterInterval = 5 * time.Millisecond
		o.FetchRoster = func() ([]RosterEntry, error) {
			return []RosterEntry{{Harp: "swift-elm-fox", State: "executing", LastActivityUnix: 1}}, nil
		}
	})
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "roster digest on the bar", func() bool {
		return strings.Contains(h.tty.String(), "swift-elm-fox→executing")
	})
	h.c.Close()
}

// TestController_CloseJoinsRosterPoll forces the interleaving rapid-grass is
// about: the poll goroutine is held mid-iteration inside FetchRoster when
// Close runs. Close must not return until that goroutine has exited —
// otherwise the rest of the iteration (SetRoster, which reads the
// package-level nowNanos seam) runs after Close, racing whatever the caller
// does next, such as a later test swapping that seam.
func TestController_CloseJoinsRosterPoll(t *testing.T) {
	entered := make(chan struct{})
	closeReturned := make(chan struct{})
	var once sync.Once
	h := newCtlHarness(t, func(o *Options) {
		o.RosterInterval = time.Hour // only the immediate first fetch runs
		o.FetchRoster = func() ([]RosterEntry, error) {
			once.Do(func() { close(entered) })
			// Hold the poll mid-iteration. A Close that does not join
			// returns while this is still held; one that joins waits the
			// hold out. The timer bounds only the joining (green) path.
			select {
			case <-closeReturned:
			case <-time.After(100 * time.Millisecond):
			}
			return []RosterEntry{{Harp: "h", State: "executing"}}, nil
		}
	})
	<-entered
	h.c.Close()
	// Checked BEFORE releasing the hold: releasing first would let a leaked
	// goroutine finish and mask the missing join.
	defer close(closeReturned)
	select {
	case <-h.c.rosterDone:
	default:
		t.Fatal("Close returned while the roster poll goroutine was still mid-iteration")
	}
}

// TestController_RosterFetchWarnsAfterConsecutiveFailures pins the safe half:
// pollRoster discarded every FetchRoster error forever, with no
// counter and no eventual warning, so a permanently broken coordinator
// connection was indistinguishable from a stable roster. The last-good
// snapshot must still stay displayed (unchanged, documented intent), but
// enough consecutive failures must now surface exactly one warning, and a
// later success must reset the streak. Exercises rosterFetch directly
// (no goroutine/ticker) to stay fully deterministic.
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

// engageFake engages the fake overlay and returns the geometry it was given.
func engageFake(t *testing.T, h *ctlHarness) OverlayGeometry {
	t.Helper()
	_, err := h.stdinW.Write([]byte{testPrefix, 'j'})
	require.NoError(t, err)
	select {
	case geo := <-h.overlay.started:
		return geo
	case <-time.After(2 * time.Second):
		t.Fatal("overlay never started")
		return OverlayGeometry{}
	}
}

// An engine already on the alternate screen cannot be kept by moving the
// overlay to the alternate screen — leaving it would take the engine off its
// own. The overlay draws over it in place instead, release clears the panel
// region, and the overlay is told not to switch screens itself.
func TestController_EngineOnAltScreenIsDrawnOverInPlace(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })
	_, _ = h.c.Stdout().Write([]byte("\x1b[?1049h"))
	engagedAt := len(h.tty.String())

	geo := engageFake(t, h)
	assert.True(t, geo.EngineOnAltScreen, "the overlay must know it may not switch screens")
	engage := h.tty.String()[engagedAt:]
	assert.Contains(t, engage, "\x1b7\x1b[r", "the engine's cursor is saved and the overlay drawn in place")
	assert.NotContains(t, engage, "?1049h", "the overlay must not enter an alternate screen of its own")

	h.overlay.release <- nil
	h.drainTranslated(t) // the repaint nudge is what redraws the engine here
	out := h.tty.String()[engagedAt:]
	assert.Contains(t, out, "\x1b[16;1H\x1b[J", "release clears the panel region it drew over")
	assert.NotContains(t, out, "?1049l", "release must not take the engine off its alternate screen")
}

// A session that ends while the viewer is open still gets its own screen
// back: Close owns the final restore, but only release knows the overlay
// moved to the alternate screen.
func TestController_CloseDuringEngagementLeavesTheAltScreen(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })
	engageFake(t, h)
	engagedAt := strings.LastIndex(h.tty.String(), "\x1b[?1049h")
	require.GreaterOrEqual(t, engagedAt, 0)

	h.c.Close()
	assert.Contains(t, h.tty.String()[engagedAt:], "\x1b[?1049l", "closing mid-engagement returns to the engine's screen")
}

func TestOverflowNotice_NeverWraps(t *testing.T) {
	assert.Equal(t, "\x1b[7m"+overflowText+"\x1b[0m", string(overflowNotice(200)))
	assert.Equal(t, "\x1b[7m"+overflowText[:20]+"\x1b[0m", string(overflowNotice(20)), "cut to the width")
}
