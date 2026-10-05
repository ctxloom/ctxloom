package termui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a goroutine-safe tty stand-in. Every write is an event
// waitUntil wakes on.
type lockedBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written signal
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	b.written.fire()
	return n, err
}

// waitUntil blocks until what has been written satisfies cond, re-checking on
// every write. For bytes a goroutine the test does not drive writes; where
// the code under test orders a write before an event the test can receive,
// receive that instead and assert.
func (b *lockedBuffer) waitUntil(t *testing.T, what string, cond func(string) bool) {
	t.Helper()
	expired := expiry(t)
	for {
		written := b.written.wait()
		if cond(b.String()) {
			return
		}
		select {
		case <-written:
		case <-expired:
			t.Fatalf("never saw %s; written: %q", what, b.String())
		}
	}
}

// expiry bounds a wait on an event by the test binary's own deadline, less
// enough to name the event that never came. No wait carries a deadline of its
// own: one short enough to matter expires on an event that is merely late on
// a loaded machine, which is a failure of the test, not of the code.
func expiry(t *testing.T) <-chan time.Time {
	d, ok := t.Deadline()
	if !ok {
		return nil
	}
	return time.After(time.Until(d) - 10*time.Second)
}

// await receives the event ch carries, failing only at the test's deadline.
func await[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-expiry(t):
		t.Fatalf("never received %s", what)
		var zero T
		return zero
	}
}

// stdinFeed is the harness's terminal input: each typed string is one read,
// and typing returns once the interceptor has come back for the next read —
// by then everything the chunk did on the stdin pump (the scan, an engage,
// routing to the viewer's sink, the engine's copy) has happened.
type stdinFeed struct {
	chunks chan []byte
	done   chan struct{} // the reader finished a chunk and wants the next
	closed chan struct{}
	once   sync.Once
	// Touched only by the reading goroutine.
	rest    []byte
	pending bool // a chunk was delivered and not yet acknowledged
}

func newStdinFeed() *stdinFeed {
	return &stdinFeed{chunks: make(chan []byte), done: make(chan struct{}), closed: make(chan struct{})}
}

func (f *stdinFeed) Read(p []byte) (int, error) {
	if len(f.rest) == 0 {
		if f.pending {
			select {
			case f.done <- struct{}{}:
			case <-f.closed:
				return 0, io.EOF
			}
			f.pending = false
		}
		select {
		case f.rest = <-f.chunks:
			f.pending = true
		case <-f.closed:
			return 0, io.EOF
		}
	}
	n := copy(p, f.rest)
	f.rest = f.rest[n:]
	return n, nil
}

func (f *stdinFeed) close() { f.once.Do(func() { close(f.closed) }) }

// typeKeys delivers s as one read and returns once the reader has finished
// with it.
func (f *stdinFeed) typeKeys(t *testing.T, s string) {
	t.Helper()
	select {
	case f.chunks <- []byte(s):
	case <-expiry(t):
		t.Fatalf("the stdin reader never took %q", s)
	}
	await(t, "the stdin reader back for more after "+strconv.Quote(s), f.done)
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

// ctlHarness assembles a controller over a typed stdin feed, a locked tty, a
// test resize source and a fake clock, with a pump goroutine standing in for
// the plugin client's stdin pump.
type ctlHarness struct {
	c       *Controller
	stdin   *stdinFeed
	tty     *lockedBuffer
	src     chan *agent.WindowSize
	clk     *armClock
	engine  lockedBuffer // what the engine "receives" from the pump
	warns   chan string
	overlay *fakeOverlay
	pumpEnd chan struct{}
	nudges  int // restores moved by nudgeRestored
}

func newCtlHarness(t *testing.T, mutate func(*Options)) *ctlHarness {
	t.Helper()
	h := &ctlHarness{
		stdin:   newStdinFeed(),
		tty:     &lockedBuffer{},
		src:     make(chan *agent.WindowSize, 4),
		clk:     &armClock{Clock: fakeclock.New()},
		warns:   make(chan string, 4),
		overlay: newFakeOverlay(),
		pumpEnd: make(chan struct{}),
	}
	opts := Options{
		Stdin:      h.stdin,
		TTY:        h.tty,
		Resize:     h.src,
		Prefix:     testPrefix,
		Surround:   true,
		Bar:        BarInfo{Harp: "perky-same-chevy", Engine: "claude-code", PrefixHint: "^]"},
		NewOverlay: func(OverlayStart) Overlay { return h.overlay },
		Warn:       func(format string, args ...any) { h.warns <- fmt.Sprintf(format, args...) },
		Clock:      h.clk,
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
		// test and keep writing to its tty after the test has returned.
		h.c.Close()
		h.stdin.close()
		close(h.src)
		<-h.pumpEnd
	})
	return h
}

// typeKeys is one read of the terminal, returned from once the stdin pump has
// done everything that read asks of it.
func (h *ctlHarness) typeKeys(t *testing.T, s string) {
	t.Helper()
	h.stdin.typeKeys(t, s)
}

// sized delivers the terminal's size and drains its translation. The surround
// establishes its region before the translated size is sent, so it is on the
// tty when this returns.
func (h *ctlHarness) sized(t *testing.T, rows, cols uint16) *agent.WindowSize {
	t.Helper()
	h.src <- &agent.WindowSize{Rows: rows, Cols: cols}
	ws := h.drainTranslated(t)
	require.Contains(t, h.tty.String(), fmt.Sprintf("\x1b[1;%dr", rows-1), "the surround's region")
	return ws
}

// nudgeRestored moves the clock past the nudge's wiggle separation and
// returns the size the nudge settles on. Nudge sends the wiggle and then arms
// the restore, on the releasing goroutine: the clock may move only once that
// timer exists, or the restore is armed after the move and never fires.
func (h *ctlHarness) nudgeRestored(t *testing.T) *agent.WindowSize {
	t.Helper()
	h.nudges++
	h.clk.waitArmed(t, nudgeWiggleSeparation, h.nudges)
	h.clk.Advance(nudgeWiggleSeparation)
	return h.drainTranslated(t)
}

// armClock is a fake clock whose timer arming and stopping are events a test
// can wait on: a goroutine the test does not drive (Summon's retry) reaching
// the clock is the moment the test may move it.
type armClock struct {
	*fakeclock.Clock
	changed signal
	mu      sync.Mutex
	armed   map[time.Duration]int // every timer ever armed, by duration
}

func (c *armClock) AfterFunc(d time.Duration, f func()) func() bool {
	stop := c.Clock.AfterFunc(d, f)
	c.mu.Lock()
	if c.armed == nil {
		c.armed = map[time.Duration]int{}
	}
	c.armed[d]++
	c.mu.Unlock()
	c.changed.fire()
	return func() bool {
		stopped := stop()
		c.changed.fire()
		return stopped
	}
}

// waitArmed blocks until n timers of duration d have been armed in all.
func (c *armClock) waitArmed(t *testing.T, d time.Duration, n int) {
	t.Helper()
	c.waitClock(t, fmt.Sprintf("%d timers of %v armed", n, d), func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.armed[d] >= n
	})
}

// waitPending blocks until exactly n timers are armed.
func (c *armClock) waitPending(t *testing.T, n int) {
	t.Helper()
	c.waitClock(t, fmt.Sprintf("%d pending timers", n), func() bool { return c.Pending() == n })
}

func (c *armClock) waitClock(t *testing.T, what string, cond func() bool) {
	t.Helper()
	expired := expiry(t)
	for {
		changed := c.changed.wait()
		if cond() {
			return
		}
		select {
		case <-changed:
		case <-expired:
			t.Fatalf("never saw %s", what)
		}
	}
}

func (h *ctlHarness) drainTranslated(t *testing.T) *agent.WindowSize {
	return recvSize(t, h.c.Resize())
}

func TestController_EngageHoldReplayNudge(t *testing.T) {
	h := newCtlHarness(t, nil)
	ws := h.sized(t, 24, 80)
	require.Equal(t, uint16(23), ws.Rows, "initial size reaches the engine reserved")

	// Engage: prefix + a viewer key.
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	geo := await(t, "the overlay's start", h.overlay.started)
	assert.Equal(t, 23, geo.Rows, "geo.Rows is the DRAWABLE height (real 24 rows minus the surround's 1-row reservation)")
	assert.Equal(t, 80, geo.Cols)
	assert.Equal(t, 8, geo.PanelRows, "bottom third floored at 8 rows")
	assert.Contains(t, h.tty.String(), "\x1b[?1049h\x1b[r",
		"engage moves to the alternate screen (saving the engine's screen and cursor) with the full scroll region")
	h.overlay.ui.waitUntil(t, "the viewer key", func(s string) bool { return s == "j" })

	// Engine output during engagement is held.
	before := h.tty.String()
	_, _ = h.c.Stdout().Write([]byte("HELD-OUTPUT"))
	assert.NotContains(t, h.tty.String(), "HELD-OUTPUT")
	assert.Equal(t, before, h.tty.String(), "nothing hits the tty while held")

	// Disengage: one atomic restore — back to the engine's screen, scroll
	// region + bar re-established, engine cursor restored — then the replay,
	// then a nudge.
	// The nudge is sent after the restore and the replay are written.
	h.overlay.release <- nil
	first := h.drainTranslated(t)
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

	second := h.nudgeRestored(t)
	assert.Equal(t, uint16(22), first.Rows, "repaint nudge wiggles a row")
	assert.Equal(t, uint16(23), second.Rows)

	// interceptor is back to passthrough.
	h.typeKeys(t, "typed-after")
	assert.Equal(t, "typed-after", h.engine.String(), "passthrough restored")
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
	h.sized(t, 24, 80)

	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	geo := await(t, "the overlay's start", h.overlay.started)

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
	h.sized(t, 24, 80)

	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	await(t, "the overlay's start", h.overlay.started)

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
	h.drainTranslated(t) // the release's nudge: its resume is on the tty
	assert.Contains(t, h.tty.String(), "\x1b[1;29r", "resumed with the new size")
}

func TestController_DoublePressLiteralAbortsOverlay(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.sized(t, 24, 80)

	// Two writes → two read chunks → engage fires, then the literal aborts it.
	h.typeKeys(t, string([]byte{testPrefix}))
	await(t, "the overlay's start", h.overlay.started)
	h.typeKeys(t, string([]byte{testPrefix}))
	assert.Equal(t, string(testPrefix), h.engine.String(), "the literal prefix reaches the engine")
	select {
	case <-h.overlay.aborts:
	default:
		t.Fatal("the literal did not abort the overlay") // AbortLiteral runs on the pump
	}
}

func TestController_OverlayErrorDegradesToPlainTerminal(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.sized(t, 24, 80)

	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	<-h.overlay.started
	h.overlay.release <- fmt.Errorf("boom")

	w := await(t, "the degradation warning", h.warns)
	assert.Contains(t, w, "plain terminal")
	assert.Contains(t, w, "boom")

	// The prefix now passes through — the session must survive the UI.
	h.typeKeys(t, string([]byte{testPrefix, 'x'}))
	assert.Equal(t, string(testPrefix)+"x", h.engine.String(), "degraded passthrough")
}

func TestController_FactoryPanicDegrades(t *testing.T) {
	h := newCtlHarness(t, func(o *Options) {
		o.NewOverlay = func(OverlayStart) Overlay { panic("factory exploded") }
	})
	h.sized(t, 24, 80)

	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	select {
	case w := <-h.warns:
		assert.Contains(t, w, "factory exploded")
	default:
		t.Fatal("no warning for the factory panic") // the factory runs on the pump
	}
	h.typeKeys(t, "still-typing")
	assert.Contains(t, h.engine.String(), "still-typing", "the engine keeps receiving input")
}

func TestController_CloseRestoresTerminal(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.sized(t, 24, 80)

	h.c.Close()
	out := h.tty.String()
	assert.Contains(t, out, "\x1b[r", "full scroll region restored on exit")
	assert.True(t, strings.HasSuffix(out, "\x1b[24;1H\x1b[2K"), "bar row cleared last")

	h.c.Close() // idempotent
}

func TestController_CloseWhileEngagedFlushesHeldOutput(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.sized(t, 24, 80)

	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
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
	h := newCtlHarness(t, nil)
	clk := h.clk
	h.sized(t, 24, 80)
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
	h := newCtlHarness(t, nil)
	clk := h.clk
	h.sized(t, 24, 80)

	h.c.sur.Suspend()
	h.c.SetApprovals(1, clk.Now(), true)
	assert.NotContains(t, h.tty.String(), "\a", "no bell while the bar is suspended")
	_ = h.c.sur.ResumeSequence()
	h.c.SetApprovals(2, clk.Now(), true)
	assert.Contains(t, h.tty.String(), "\a")
}

// TestController_RingRingsOnceWhileTheBarShows: Ring is one bell for an
// event that needs the human, rung only while the bar shows (a modal or an
// overlay on screen is its own signal) and never folded into the approval
// bell's interval — the caller rings once per event, and an approval bell a
// moment earlier must not swallow it.
func TestController_RingRingsOnceWhileTheBarShows(t *testing.T) {
	h := newCtlHarness(t, nil)
	clk := h.clk
	h.sized(t, 24, 80)
	bells := func() int { return strings.Count(h.tty.String(), "\a") }

	h.c.SetApprovals(1, clk.Now(), true)
	require.Equal(t, 1, bells())
	assert.True(t, h.c.Ring(), "an approval bell a moment earlier does not swallow it")
	assert.Equal(t, 2, bells())

	h.c.sur.Suspend()
	assert.False(t, h.c.Ring(), "no bell while the bar is suspended")
	assert.Equal(t, 2, bells())
}

// TestController_WithoutABarAnnounceAndRingReachTheTerminal: with the bar
// off (ui.surround: false) nothing paints a note, so Announce writes its line
// to the terminal itself, on a line of its own between engine output, and Ring
// still rings — once per call.
func TestController_WithoutABarAnnounceAndRingReachTheTerminal(t *testing.T) {
	h := newCtlHarness(t, func(o *Options) { o.Surround = false })
	_, _ = h.c.Stdout().Write([]byte("engine says hi"))
	h.c.Announce("CREDENTIAL REFUSED: claude (TOKEN): 2 parked")
	assert.Contains(t, h.tty.String(), "engine says hi\r\nCREDENTIAL REFUSED: claude (TOKEN): 2 parked\r\n")
	assert.True(t, h.c.Ring(), "the bell rings with no bar to show")
	assert.Equal(t, 1, strings.Count(h.tty.String(), "\a"))
}

// TestOutputGate_InjectWaitsBehindAHold: a line injected while an overlay
// holds the engine's output is held with it and replayed in order, never
// written over the overlay.
func TestOutputGate_InjectWaitsBehindAHold(t *testing.T) {
	var mu sync.Mutex
	dst := &lockedBuffer{}
	g := newOutputGate(realClock{}, &mu, dst, nil, nil)
	g.Hold(1 << 10)
	_, _ = g.Write([]byte("held engine bytes"))
	g.Inject([]byte("\r\nNOTICE\r\n"))
	assert.Empty(t, dst.String(), "nothing reaches the overlay's screen")
	_, err := g.Release(holdReplay, restore{})
	require.NoError(t, err)
	assert.Equal(t, "held engine bytes\r\nNOTICE\r\n", dst.String())
	g.Inject([]byte("open"))
	assert.Equal(t, "held engine bytes\r\nNOTICE\r\nopen", dst.String())
}

func TestController_RosterPollFeedsBar(t *testing.T) {
	h := newCtlHarness(t, func(o *Options) {
		o.RosterInterval = time.Hour // the poll's immediate first fetch is the one under test
		o.FetchRoster = func() ([]RosterEntry, error) {
			return []RosterEntry{{Harp: "swift-elm-fox", State: "executing", LastActivityUnix: 1}}, nil
		}
	})
	h.sized(t, 24, 80)
	// The poll paints on its own goroutine; its paint is the event.
	h.tty.waitUntil(t, "the roster digest on the bar", func(s string) bool {
		return strings.Contains(s, "swift-elm-fox→executing")
	})
	h.c.Close()
}

// TestController_CloseJoinsRosterPoll forces the interleaving rapid-grass is
// about: the poll goroutine is held mid-iteration inside FetchRoster when
// Close runs. Close must not return until that goroutine has exited —
// otherwise the rest of the iteration (SetRoster, which paints the bar to the
// caller's tty) runs after Close, racing whatever the caller does next.
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
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	h.typeKeys(t, "after")
	assert.Contains(t, h.engine.String(), "after", "passthrough after the refused engage")
	select {
	case <-h.overlay.started:
		t.Fatal("overlay must not start without a known terminal size")
	default:
	}
}

// engageFake engages the fake overlay and returns the geometry it was given.
func engageFake(t *testing.T, h *ctlHarness) OverlayGeometry {
	t.Helper()
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	return await(t, "the overlay's start", h.overlay.started)
}

// An engine already on the alternate screen cannot be kept by moving the
// overlay to the alternate screen — leaving it would take the engine off its
// own. The overlay draws over it in place instead, release clears the panel
// region, and the overlay is told not to switch screens itself.
func TestController_EngineOnAltScreenIsDrawnOverInPlace(t *testing.T) {
	h := newCtlHarness(t, nil)
	h.sized(t, 24, 80)
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
	h.sized(t, 24, 80)
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
