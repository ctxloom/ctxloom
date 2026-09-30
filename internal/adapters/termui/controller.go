package termui

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// Overlay is a viewer the controller hands the terminal to: prefix-opened,
// or summoned by code (Summon). The bubbletea implementation lives in
// internal/adapters/cli/tui; this package only orchestrates its lifecycle so
// the hot path never links the TUI framework, and tests drive the controller
// with a fake.
type Overlay interface {
	// Run blocks until the overlay exits (user backed out, or Abort). input
	// delivers the viewer-routed keystrokes; tty is the raw terminal writer
	// (engine output is held for the duration, the surround suspended).
	Run(input io.Reader, tty io.Writer, geo OverlayGeometry) error
	// Abort asks a running overlay to exit — the double-press-literal path.
	// Must be safe before/after Run.
	Abort()
	// Resize relays out a running overlay for a new terminal size (the
	// geometry is recomputed exactly as for Run). Must be safe before/after
	// Run.
	Resize(geo OverlayGeometry)
	// Armed ends a summoned overlay's inert window: keys reach it from here
	// on. discarded is how many keys the window swallowed.
	Armed(discarded int)
	// Notify tells an engaged overlay that something asked for the screen
	// (Summon) and was not given it.
	Notify(n Notice)
}

// Notice is what Notify delivers.
type Notice struct{ Text string }

// OverlayStart says how an engagement began.
type OverlayStart struct {
	// Summoned: opened by code, not the prefix — full screen from the first
	// frame, and the arming window applies.
	Summoned bool
	// View names the view to open: "" roster/feed, "approvals".
	View string
}

// OverlayFactory builds a fresh overlay per engagement.
type OverlayFactory func(OverlayStart) Overlay

var (
	// ErrUIUnavailable: the terminal layer is degraded or closed.
	ErrUIUnavailable = errors.New("termui: terminal layer unavailable")
	// ErrOverlayEngaged: an overlay already holds the terminal; it was told
	// (Notify) instead.
	ErrOverlayEngaged = errors.New("termui: an overlay is already engaged")
)

// OverlayGeometry is the overlay's DRAWABLE terminal size: Rows is the real
// terminal's row count with the surround's reserved bottom row already
// subtracted (Cols is unaffected — the reservation is rows-only), exactly
// like the engine's own viewport (resizeTranslator.Translate). PanelRows is
// the quick panel's height budget on top of that. Because Rows already
// excludes the reserved row, neither the panel (rows Rows−PanelRows+1..Rows)
// nor a full-screen presentation (whose content height is Rows) can ever
// address it. The controller owns the panel-region policy so it can clear
// exactly that region on disengage.
type OverlayGeometry struct {
	Cols, Rows int
	// PanelRows is the quick panel's height: the overlay's bottom PanelRows
	// rows of the DRAWABLE screen (Rows above).
	PanelRows int
	// EngineOnAltScreen: the engine is itself on the alternate screen, so
	// the overlay draws over it in place (takeScreen). Leaving an alternate
	// screen there would take the engine off its own, so the overlay must not
	// enter one of its own for full screen.
	EngineOnAltScreen bool
}

// panelRows is the quick-panel height policy: the bottom third, at least 8
// rows, never the whole screen.
func panelRows(rows int) int {
	p := rows / 3
	if p < 8 {
		p = 8
	}
	if p > rows-1 {
		p = rows - 1
	}
	if p < 1 {
		p = 1
	}
	return p
}

// Options configures the terminal layer for one interactive run.
type Options struct {
	Stdin  io.Reader                // the real tty reader (raw mode already set)
	TTY    io.Writer                // the real tty writer
	Resize <-chan *agent.WindowSize // the frontend's SIGWINCH channel (watchResize)

	Prefix     byte // from ParsePrefixKey
	Surround   bool // persistent bar on reserved bottom row
	Bar        BarInfo
	NewOverlay OverlayFactory

	// FetchRoster polls orchestrator-held children for the bar digest; nil
	// disables polling. RosterInterval defaults to 2s.
	FetchRoster    func() ([]RosterEntry, error)
	RosterInterval time.Duration

	// Warn streams UI-layer degradation notices (never fatal). nil = silent.
	Warn func(format string, args ...any)

	// HoldCapacity bounds the engine output held under a prefix-opened
	// overlay; default 256 KiB.
	HoldCapacity int
	// ModalHoldCapacity bounds it under a summoned one, which may stay up for
	// as long as an approval's timeout while the engine keeps working;
	// default 8 MiB. Both grow with what is held. Past the bound the hold is
	// dropped and the screen redrawn on release.
	ModalHoldCapacity int

	// Present times the summoned modal's focus locks.
	Present PresentPolicy
	// Clock is nil for real time; tests inject one.
	Clock Clock
}

// Controller wires the interceptor, surround, output gate, and resize
// translation around the plugin client's existing seams. Construct with New,
// hand Stdin/Stdout/Resize to client.Run, and Close on every exit path.
type Controller struct {
	opts    Options
	clock   Clock
	present PresentPolicy
	ttyMu   sync.Mutex

	ic    *interceptor
	gate  *outputGate
	guard *vtGuard
	sur   *surround
	rt    *resizeTranslator

	// session serializes engagements: held from engage until the overlay's
	// teardown finishes, so a re-engage cannot overlap a release in flight.
	session sync.Mutex
	// overlayMu guards eng and gen; eng is the live engagement (nil when
	// none), published once built and never mutated after.
	overlayMu sync.Mutex
	eng       *engagement
	gen       uint64
	// changed fires when an engagement starts or ends and when the layer
	// degrades or closes — the wake-up for a waiting Summon.
	changed signal

	bellMu   sync.Mutex
	lastBell time.Time

	uiOff  atomic.Bool
	closed atomic.Bool
	done   chan struct{} // closed by Close
	// rosterDone is closed when pollRoster's goroutine has exited (at once
	// when there is no poller); Close waits on it so no poll iteration can
	// touch the surround after Close returns.
	rosterDone chan struct{}

	// rosterFails counts consecutive FetchRoster failures; touched only from
	// pollRoster's own goroutine, never concurrently.
	rosterFails int
}

// engagement is one overlay's hold on the terminal.
type engagement struct {
	ov    Overlay
	start OverlayStart
	geo   OverlayGeometry
	tk    takeover
	gen   uint64
}

// rosterFailWarnThreshold is how many consecutive FetchRoster failures
// pollRoster tolerates silently before surfacing exactly one warning:
// the last-good roster snapshot intentionally stays displayed
// either way, but a permanently broken coordinator connection must not stay
// indistinguishable from a stable one forever.
const rosterFailWarnThreshold = 3

// New builds the terminal layer. The caller guarantees Stdin/TTY are the real
// terminal (never wrap a pipe) and that raw mode is already established.
func New(opts Options) *Controller {
	if opts.RosterInterval <= 0 {
		opts.RosterInterval = 2 * time.Second
	}
	if opts.HoldCapacity <= 0 {
		opts.HoldCapacity = 256 << 10
	}
	if opts.ModalHoldCapacity <= 0 {
		opts.ModalHoldCapacity = 8 << 20
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	c := &Controller{
		opts: opts, clock: opts.Clock, present: opts.Present.normalized(),
		done: make(chan struct{}), rosterDone: make(chan struct{}),
	}
	c.sur = newSurround(&c.ttyMu, opts.TTY, opts.Surround, opts.Bar)
	// The guard runs inside the gate under the shared tty lock; its callbacks
	// are the surround's *Locked accessors (same mutex, no re-entry).
	guard := newVTGuard(c.sur.regionBottomLocked, c.sur.reassertLocked, c.sur.markDirtyLocked)
	c.guard = guard
	c.gate = newOutputGate(&c.ttyMu, opts.TTY, guard, c.sur.FlushLocked)
	// SetEngineIdle/SetPaintSafe inlined: construction-only writes, before any
	// goroutine starts.
	c.sur.lastEngineWrite = c.gate.LastWriteNanos
	c.sur.paintSafe = guard.SafeForPaint
	// Erase the primary screen once at terminal takeover. The session banner and
	// startup lines render as plain text on the primary screen; the surround's
	// DECSTBM (emitted by SetSize below) homes the cursor but erases nothing, and
	// the child engine then paints its onboarding prompts with cursor-addressed
	// writes that leave those characters in the cells they don't touch —
	// producing a cell-level interleave. Clear here, before the bar is painted, so
	// the surround and child both draw onto a clean screen. ED 2 (not 3) keeps the
	// that output in scrollback rather than nuking it.
	_, _ = io.WriteString(opts.TTY, "\x1b[H\x1b[2J")
	c.rt = newResizeTranslator(opts.Resize, c.sur.reserve, c.onSize, c.clock)
	c.ic = newInterceptor(opts.Stdin, opts.Prefix, InterceptorCallbacks{
		Engage:       c.engage,
		AbortLiteral: c.abortLiteral,
	}, c.clock.Now)
	if opts.FetchRoster != nil {
		go c.pollRoster()
	} else {
		close(c.rosterDone)
	}
	return c
}

// Stdin is the engine-bound keystroke stream (prefix intercepted).
func (c *Controller) Stdin() io.Reader { return c.ic }

// Stdout is the engine-output writer (held while the viewer is engaged).
func (c *Controller) Stdout() io.Writer { return c.gate }

// Resize is the translated size channel for client.Run.
func (c *Controller) Resize() <-chan *agent.WindowSize { return c.rt.Out() }

// Close restores the terminal on every exit path: aborts a live overlay,
// flushes any held engine output, and hands back the full scroll region with
// the bar row cleared. Idempotent — callers compose it with the raw-mode
// restore and may invoke it from both the deferred and the inline path.
func (c *Controller) Close() {
	if c.closed.Swap(true) {
		return
	}
	close(c.done)
	c.changed.fire()
	// Join, not just signal: a ticker that already fired races the closed
	// channel, so one more poll iteration may be in flight; it must finish
	// before the restore below and before Close returns.
	<-c.rosterDone
	if e := c.engaged(); e != nil {
		e.ov.Abort()
	}
	// Serialize with a teardown in flight so the overlay's release can't
	// repaint after the final restore.
	c.session.Lock()
	defer c.session.Unlock()
	if _, err := c.gate.Release(holdReplay, restore{}); err != nil {
		// A failing tty on the final restore used to silently lose
		// the whole held-output replay; surface it instead of swallowing it a
		// second time here.
		c.warn("output gate release: %v", err)
	}
	if err := c.gate.FlushGuard(); err != nil {
		// Bytes the guard held back (a pending incomplete
		// escape/CSI sequence or split UTF-8 rune tail) have no other flush
		// path; the session is ending, so nothing else will ever complete
		// them. Teardown-only — see FlushGuard's doc comment.
		c.warn("output gate flush: %v", err)
	}
	c.sur.Restore()
}

func (c *Controller) warn(format string, args ...any) {
	if c.opts.Warn != nil {
		c.opts.Warn(format, args...)
	}
}

// degrade permanently disables the UI layer after a viewer failure: the
// session continues on a plain terminal, keystrokes and output untouched.
func (c *Controller) degrade(err error) {
	c.uiOff.Store(true)
	c.ic.Off()
	c.changed.fire()
	c.warn("terminal viewer failed; continuing with a plain terminal (engine session unaffected): %v", err)
}

// engage opens the prefix viewer: hold engine output, suspend the bar, take
// the screen, and hand the interceptor the overlay's input sink. Runs on the
// stdin pump goroutine (inside interceptor.Read's dispatch); the overlay
// itself runs on its own goroutine. Returns nil when the viewer cannot start
// — the interceptor then drops back to passthrough.
func (c *Controller) engage() io.Writer {
	if c.unavailable() {
		return nil
	}
	// Serialize with a previous engagement's teardown (released by runOverlay).
	c.session.Lock()
	e, err := c.prepare(OverlayStart{})
	if e == nil {
		c.session.Unlock()
		if err != nil {
			c.degrade(err)
		}
		c.changed.fire()
		return nil
	}
	pr, pw := io.Pipe()
	c.show(e, pr)
	return pw
}

func (c *Controller) unavailable() bool {
	return c.uiOff.Load() || c.closed.Load() || c.opts.NewOverlay == nil
}

// prepare builds one engagement's overlay and geometry without touching the
// screen. Called with the session held. A nil engagement with a nil error
// means there is no terminal size yet to lay anything out in.
func (c *Controller) prepare(start OverlayStart) (*engagement, error) {
	ov, err := c.buildOverlay(start)
	if err != nil {
		return nil, err
	}
	rows, cols := c.rt.Current()
	if rows == 0 {
		return nil, nil
	}
	c.overlayMu.Lock()
	c.gen++
	gen := c.gen
	c.overlayMu.Unlock()
	return &engagement{ov: ov, start: start, geo: c.geometry(rows, cols), gen: gen}, nil
}

// geometry is the overlay's DRAWABLE area for a real terminal size: the rows
// the engine's own viewport gets (the same reservation predicate
// resizeTranslator.Translate applies), so overlay content can never reach
// the surround's reserved bottom row.
func (c *Controller) geometry(rows, cols int) OverlayGeometry {
	if reserveActive(rows, c.sur.reserve) {
		rows -= c.sur.reserve
	}
	return OverlayGeometry{Cols: cols, Rows: rows, PanelRows: panelRows(rows)}
}

// show hands the terminal to a prepared engagement and starts its overlay.
// The session is held and stays held until runOverlay's teardown.
func (c *Controller) show(e *engagement, pr *io.PipeReader) {
	c.sur.Suspend()
	capacity := c.opts.HoldCapacity
	if e.start.Summoned {
		capacity = c.opts.ModalHoldCapacity
	}
	c.gate.Hold(capacity)
	// Take the screen, and hand the overlay the FULL scroll region: with the
	// surround's DECSTBM still active, the overlay's bottom-row repaints would
	// scroll the screen one line per frame. Release re-establishes the region.
	c.ttyMu.Lock()
	e.tk = takeScreen(c.guard.altScreen, e.start.Summoned)
	_, _ = c.opts.TTY.Write(e.tk.enter)
	c.ttyMu.Unlock()
	e.geo.EngineOnAltScreen = e.tk.leave == nil
	c.overlayMu.Lock()
	c.eng = e
	c.overlayMu.Unlock()
	c.changed.fire()
	go c.runOverlay(e, pr)
}

// engaged returns the live engagement, if any.
func (c *Controller) engaged() *engagement {
	c.overlayMu.Lock()
	defer c.overlayMu.Unlock()
	return c.eng
}

// takeover is how one engagement takes the screen from the engine and how
// release gives it back.
type takeover struct {
	enter []byte
	// leave returns to the engine's screen; nil when the overlay drew over it
	// in place.
	leave []byte
}

// takeScreen picks the takeover for an engine on the main screen or, when
// engineOnAlt, on the alternate one.
//
// The overlay goes to the ALTERNATE screen: 1049 saves the cursor and leaves
// every cell of the engine's screen as it was, so leaving it IS the restore —
// for any engine, including one that only writes lines and has nothing to
// repaint on a nudge. Drawing in place instead erased whatever the panel
// covered, and nothing brought it back.
//
// An engine already on the alternate screen cannot be kept that way: 1049
// would pull it off its own screen. There the overlay draws in place over it
// (the engine's cursor saved by DECSC), release clears the panel region, and
// the nudge that follows is what repaints it — which a full-screen program,
// the only kind that uses the alternate screen, does on a resize.
//
// A summoned overlay is full screen from its first frame, so over an engine
// on the alternate screen it starts from a cleared one.
func takeScreen(engineOnAlt, full bool) takeover {
	switch {
	case engineOnAlt && full:
		return takeover{enter: []byte("\x1b7\x1b[r\x1b[H\x1b[2J")}
	case engineOnAlt:
		return takeover{enter: []byte("\x1b7\x1b[r")}
	}
	// Leaving restores the engine's cursor; it is saved again at once because
	// release's region reset homes the cursor and its DECRC must find the
	// engine's position in the DECSC slot — which a terminal need not share
	// with 1049's own save.
	return takeover{enter: []byte("\x1b[?1049h\x1b[r"), leave: []byte("\x1b[?1049l\x1b7")}
}

// buildOverlay isolates factory panics so a broken viewer degrades instead of
// unwinding the stdin pump.
func (c *Controller) buildOverlay(start OverlayStart) (ov Overlay, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("overlay construction panicked: %v", r)
		}
	}()
	return c.opts.NewOverlay(start), nil
}

// runOverlay hosts one engagement on its own goroutine and always tears down:
// interceptor back to passthrough, screen restored, bar resumed, engine
// nudged to repaint.
func (c *Controller) runOverlay(e *engagement, pr *io.PipeReader) {
	// Fire after the unlock (defers run last-first), so a Summon woken by it
	// finds the session free.
	defer c.changed.fire()
	defer c.session.Unlock()
	tty := c.opts.TTY
	if e.start.Summoned {
		tty = &frameWatch{w: tty, first: func() { c.startArming(e) }}
	}
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("overlay panicked: %v", r)
			}
		}()
		return e.ov.Run(pr, tty, e.geo)
	}()
	// Unblock/void the interceptor's sink writes before flipping state.
	pr.CloseWithError(io.ErrClosedPipe)
	c.overlayMu.Lock()
	c.eng = nil
	c.overlayMu.Unlock()
	c.ic.Disengage()
	c.release(e)
	if err != nil {
		c.degrade(err)
	}
}

// release restores the screen after an overlay as ONE atomic tty write (the
// gate's Release), in one of two ways:
//
//   - replay: return to the engine's screen (or clear the panel region an
//     in-place takeover drew over), re-establish the surround's scroll region
//     and bar, restore the engine's saved cursor (DECRC), then the held
//     engine output — byte-exact, against the very screen it was written for;
//   - redraw: when a summoned full-screen overlay drew over an engine on the
//     alternate screen (nothing of its screen survives to replay onto), or
//     when the hold overflowed (what survives is not whole): clear the
//     screen, re-establish region + bar + cursor, drop the held output.
//
// Either way a repaint nudge through the resize seam follows, and it is what
// brings a redrawn screen back.
func (c *Controller) release(e *engagement) {
	mode := holdReplay
	if e.start.Summoned && e.geo.EngineOnAltScreen {
		mode = holdDiscardRedraw
	}
	if c.closed.Load() {
		// Close owns the final restore; don't repaint a handed-back terminal —
		// but do hand back the engine's screen, which nothing else leaves.
		if _, err := c.gate.Release(mode, restore{replay: e.tk.leave, clear: e.tk.leave}); err != nil {
			c.warn("output gate release: %v", err)
		}
		return
	}
	resume := slices.Concat(c.sur.ResumeSequence(), []byte("\x1b8"))
	back := e.tk.leave
	if back == nil {
		back = panelClearSeq(e.geo)
	}
	r := restore{
		replay: slices.Concat(back, resume),
		clear:  slices.Concat(e.tk.leave, []byte("\x1b[H\x1b[2J")),
		notice: overflowNotice(e.geo.Cols),
		resume: resume,
	}
	if _, err := c.gate.Release(mode, r); err != nil {
		// Surface a failing tty write instead of discarding it.
		c.warn("output gate release: %v", err)
	}
	c.rt.Nudge()
}

// overflowText is said on the cleared screen when a hold overflowed: what the
// engine wrote in the meantime is gone, and anything that scrolled away with
// it is not in the scrollback.
const overflowText = "ctxloom: engine output overflowed while the overlay was open; screen redrawn"

// overflowNotice is overflowText in reverse video, cut to the width so it
// never wraps onto the engine's rows.
func overflowNotice(cols int) []byte {
	text := overflowText
	if cols > 0 && len(text) > cols {
		text = text[:cols]
	}
	return slices.Concat([]byte("\x1b[7m"), []byte(text), []byte("\x1b[0m"))
}

// panelClearSeq erases the overlay's panel region (its bottom PanelRows
// rows), for a takeover that drew over the engine's screen in place.
func panelClearSeq(geo OverlayGeometry) []byte {
	b := make([]byte, 0, 48)
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(geo.Rows-geo.PanelRows+1), 10)
	b = append(b, ";1H\x1b[J"...)
	return b
}

// abortLiteral is the cross-chunk double-press path: the interceptor already
// emitted the literal prefix byte and returned to passthrough; close the
// just-opened overlay.
func (c *Controller) abortLiteral() {
	if e := c.engaged(); e != nil {
		e.ov.Abort()
	}
}

// onSize is the resize translator's real-size hook: the surround's region
// and bar, then a running overlay's layout.
func (c *Controller) onSize(rows, cols int) {
	c.sur.SetSize(rows, cols)
	e := c.engaged()
	if e == nil {
		return
	}
	geo := c.geometry(rows, cols)
	geo.EngineOnAltScreen = e.geo.EngineOnAltScreen
	e.ov.Resize(geo)
}

// pollRoster feeds the bar's children digest at a gentle cadence. Fetch
// failures blank nothing — the last good snapshot stays; a fetch after the
// orchestrator exits keeps its final state visible.
func (c *Controller) pollRoster() {
	defer close(c.rosterDone)
	tick := time.NewTicker(c.opts.RosterInterval)
	defer tick.Stop()
	c.rosterFetch()
	for {
		select {
		case <-c.done:
			return
		case <-tick.C:
			c.rosterFetch()
		}
	}
}

// rosterFetch performs one FetchRoster call. On success it feeds the bar and
// resets the consecutive-failure streak. On failure it keeps the last-good
// snapshot but counts the streak and warns exactly once when it reaches
// rosterFailWarnThreshold — silence beyond that point used to make a
// permanently broken coordinator connection indistinguishable from a stable
// roster.
func (c *Controller) rosterFetch() {
	roster, err := c.opts.FetchRoster()
	if err != nil {
		c.rosterFails++
		if c.rosterFails == rosterFailWarnThreshold {
			c.warn("roster poll: %d consecutive failures, most recent: %v", c.rosterFails, err)
		}
		return
	}
	c.rosterFails = 0
	c.sur.SetRoster(roster)
}

// approvalBellInterval rate-limits the arrival bell: a burst of requests
// rings once, not once per request.
const approvalBellInterval = 10 * time.Second

// SetApprovals sets the bar's count of approvals waiting on the human. An
// arrival rings the bell — at most once per approvalBellInterval, and only
// while the bar is showing (a modal on screen is its own signal).
func (c *Controller) SetApprovals(n int, arrived bool) {
	c.bellMu.Lock()
	defer c.bellMu.Unlock()
	now := c.clock.Now()
	ring := arrived && (c.lastBell.IsZero() || now.Sub(c.lastBell) >= approvalBellInterval)
	if c.sur.SetApprovals(n, ring) {
		c.lastBell = now
	}
}
