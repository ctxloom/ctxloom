package termui

import (
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// Overlay is the prefix-engaged viewer. The bubbletea implementation lives in
// internal/cli/tui; this package only orchestrates its lifecycle so the hot
// path never links the TUI framework, and tests drive the controller with a
// fake.
type Overlay interface {
	// Run blocks until the overlay exits (user backed out, or Abort). input
	// delivers the viewer-routed keystrokes; tty is the raw terminal writer
	// (engine output is held for the duration, the surround suspended).
	Run(input io.Reader, tty io.Writer, geo OverlayGeometry) error
	// Abort asks a running overlay to exit — the double-press-literal path.
	// Must be safe before/after Run.
	Abort()
}

// OverlayFactory builds a fresh overlay per engagement.
type OverlayFactory func() Overlay

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
	Stdin  io.Reader             // the real tty reader (raw mode already set)
	TTY    io.Writer             // the real tty writer
	Resize <-chan *pb.WindowSize // the frontend's SIGWINCH channel (watchResize)

	Prefix     byte // from ParsePrefixKey
	Surround   bool // persistent bar on reserved bottom row
	Bar        BarInfo
	NewOverlay OverlayFactory

	// FetchRoster polls orchestrator-held children for the bar digest; nil
	// disables polling. RosterInterval defaults to 2s.
	FetchRoster    func() ([]RosterEntry, error)
	RosterInterval time.Duration

	// FetchApprovals returns how many approvals are currently parked for the
	// human (coord.Coordinator.PendingApprovals, adapted to a count so this
	// dependency-light package never sees a coord type — see doc.go). Polled
	// on the SAME cadence as FetchRoster (RosterInterval), inside the same
	// poller goroutine — no separate ticker. nil disables the indicator.
	FetchApprovals func() int

	// Warn streams UI-layer degradation notices (never fatal). nil = silent.
	Warn func(format string, args ...any)

	// HoldCapacity bounds the engine-output hold ring; default 256 KiB.
	HoldCapacity int
}

// Controller wires the interceptor, surround, output gate, and resize
// translation around the plugin client's existing seams. Construct with New,
// hand Stdin/Stdout/Resize to client.Run, and Close on every exit path.
type Controller struct {
	opts  Options
	ttyMu sync.Mutex

	ic   *interceptor
	gate *outputGate
	sur  *surround
	rt   *resizeTranslator

	// sessionMu serializes engagements: held from engage until the overlay's
	// teardown finishes, so a re-engage cannot overlap a release in flight.
	sessionMu sync.Mutex
	overlayMu sync.Mutex
	overlay   Overlay

	uiOff      atomic.Bool
	closed     atomic.Bool
	stopRoster chan struct{}

	// rosterFails counts consecutive FetchRoster failures; touched only from
	// pollRoster's own goroutine, never concurrently.
	rosterFails int
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
	c := &Controller{opts: opts, stopRoster: make(chan struct{})}
	c.sur = newSurround(&c.ttyMu, opts.TTY, opts.Surround, opts.Bar)
	// The guard runs inside the gate under the shared tty lock; its callbacks
	// are the surround's *Locked accessors (same mutex, no re-entry).
	guard := newVTGuard(c.sur.regionBottomLocked, c.sur.reassertLocked, c.sur.markDirtyLocked)
	c.gate = newOutputGate(&c.ttyMu, opts.TTY, opts.HoldCapacity, guard, c.sur.FlushLocked)
	// SetEngineIdle/SetPaintSafe inlined: construction-only writes, before any
	// goroutine starts.
	c.sur.lastEngineWrite = c.gate.LastWriteNanos
	c.sur.paintSafe = guard.SafeForPaint
	// Erase the primary screen once at terminal takeover. The session picker and
	// startup lines render as plain text on the primary screen; the surround's
	// DECSTBM (emitted by SetSize below) homes the cursor but erases nothing, and
	// the child engine then paints its onboarding prompts with cursor-addressed
	// writes that leave the picker's characters in the cells they don't touch —
	// producing a cell-level interleave. Clear here, before the bar is painted, so
	// the surround and child both draw onto a clean screen. ED 2 (not 3) keeps the
	// picker output in scrollback rather than nuking it.
	_, _ = io.WriteString(opts.TTY, "\x1b[H\x1b[2J")
	c.rt = newResizeTranslator(opts.Resize, c.sur.reserve, c.sur.SetSize)
	c.ic = newInterceptor(opts.Stdin, opts.Prefix, InterceptorCallbacks{
		Engage:       c.engage,
		AbortLiteral: c.abortLiteral,
	})
	if opts.FetchRoster != nil {
		go c.pollRoster()
	}
	return c
}

// Stdin is the engine-bound keystroke stream (prefix intercepted).
func (c *Controller) Stdin() io.Reader { return c.ic }

// Stdout is the engine-output writer (held while the viewer is engaged).
func (c *Controller) Stdout() io.Writer { return c.gate }

// Resize is the translated size channel for client.Run.
func (c *Controller) Resize() <-chan *pb.WindowSize { return c.rt.Out() }

// Close restores the terminal on every exit path: aborts a live overlay,
// flushes any held engine output, and hands back the full scroll region with
// the bar row cleared. Idempotent — callers compose it with the raw-mode
// restore and may invoke it from both the deferred and the inline path.
func (c *Controller) Close() {
	if c.closed.Swap(true) {
		return
	}
	close(c.stopRoster)
	c.overlayMu.Lock()
	ov := c.overlay
	c.overlayMu.Unlock()
	if ov != nil {
		ov.Abort()
	}
	// Serialize with a teardown in flight so the overlay's release can't
	// repaint after the final restore.
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if err := c.gate.Release(nil); err != nil {
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
	c.warn("terminal viewer failed; continuing with a plain terminal (engine session unaffected): %v", err)
}

// engage opens the viewer: hold engine output, suspend the bar, save the
// engine's cursor, and hand the interceptor the overlay's input sink. Runs on
// the stdin pump goroutine (inside interceptor.Read's dispatch); the overlay
// itself runs on its own goroutine. Returns nil when the viewer cannot start
// — the interceptor then drops back to passthrough.
func (c *Controller) engage() io.Writer {
	if c.uiOff.Load() || c.closed.Load() || c.opts.NewOverlay == nil {
		return nil
	}
	// Serialize with a previous engagement's teardown (released by runOverlay).
	c.sessionMu.Lock()
	ov, err := c.buildOverlay()
	if err != nil {
		c.sessionMu.Unlock()
		c.degrade(err)
		return nil
	}
	rows, cols := c.rt.Current()
	if rows == 0 { // no size event yet — can't lay out a panel
		c.sessionMu.Unlock()
		return nil
	}
	// The overlay draws only the DRAWABLE rows — the same
	// reservation-subtracted height the engine's own viewport gets
	// (resizeTranslator.Translate is the single source of truth for that
	// subtraction) — so its content can never reach the surround's reserved
	// bottom row.
	drawable := int(c.rt.Translate(&pb.WindowSize{Rows: uint32(rows), Cols: uint32(cols)}).Rows)
	geo := OverlayGeometry{Cols: cols, Rows: drawable, PanelRows: panelRows(drawable)}
	c.overlayMu.Lock()
	c.overlay = ov
	c.overlayMu.Unlock()
	c.sur.Suspend()
	c.gate.Hold()
	// Save the engine's cursor (DECSC), then hand the overlay the FULL
	// screen: with the surround's DECSTBM still active, the overlay's
	// bottom-row repaints would scroll the engine's held screen out from
	// under it one line per frame. Release re-establishes the region.
	c.ttyMu.Lock()
	_, _ = c.opts.TTY.Write([]byte("\x1b7\x1b[r"))
	c.ttyMu.Unlock()
	pr, pw := io.Pipe()
	go c.runOverlay(ov, pr, geo)
	return pw
}

// buildOverlay isolates factory panics so a broken viewer degrades instead of
// unwinding the stdin pump.
func (c *Controller) buildOverlay() (ov Overlay, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("overlay construction panicked: %v", r)
		}
	}()
	return c.opts.NewOverlay(), nil
}

// runOverlay hosts one engagement on its own goroutine and always tears down:
// interceptor back to passthrough, held output replayed behind the screen
// restore, bar resumed, engine nudged to repaint.
func (c *Controller) runOverlay(ov Overlay, pr *io.PipeReader, geo OverlayGeometry) {
	defer c.sessionMu.Unlock()
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("overlay panicked: %v", r)
			}
		}()
		return ov.Run(pr, c.opts.TTY, geo)
	}()
	// Unblock/void the interceptor's sink writes before flipping state.
	pr.CloseWithError(io.ErrClosedPipe)
	c.overlayMu.Lock()
	c.overlay = nil
	c.overlayMu.Unlock()
	c.ic.Disengage()
	c.release(geo)
	if err != nil {
		c.degrade(err)
	}
}

// release restores the screen after an overlay, as ONE atomic tty write via
// the gate's release preamble: clear the panel region, re-establish the
// surround's scroll region + bar, restore the engine's saved cursor (DECRC),
// then replay the held engine output (with the truncation notice on
// overflow). A repaint nudge through the resize seam follows so a
// full-screen engine redraws cleanly.
func (c *Controller) release(geo OverlayGeometry) {
	if c.closed.Load() {
		// Close owns the final restore; don't repaint a handed-back terminal.
		if err := c.gate.Release(nil); err != nil {
			c.warn("output gate release: %v", err)
		}
		return
	}
	pre := panelClearSeq(geo)
	pre = append(pre, c.sur.ResumeSequence()...)
	pre = append(pre, "\x1b8"...)
	if err := c.gate.Release(pre); err != nil {
		// Surface a failing tty write instead of discarding it.
		c.warn("output gate release: %v", err)
	}
	c.rt.Nudge()
}

// panelClearSeq erases the overlay's panel region (its bottom PanelRows
// rows). The full-screen presentation exits its alt screen before Run
// returns, so the main screen is already back; clearing the panel region
// also erases any quick-panel frame that preceded the switch to full screen.
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
	c.overlayMu.Lock()
	ov := c.overlay
	c.overlayMu.Unlock()
	if ov != nil {
		ov.Abort()
	}
}

// pollRoster feeds the bar's children digest at a gentle cadence. Fetch
// failures blank nothing — the last good snapshot stays; a fetch after the
// orchestrator exits keeps its final state visible.
func (c *Controller) pollRoster() {
	tick := time.NewTicker(c.opts.RosterInterval)
	defer tick.Stop()
	c.rosterFetch()
	for {
		select {
		case <-c.stopRoster:
			return
		case <-tick.C:
			c.rosterFetch()
		}
	}
}

// rosterFetch performs one FetchRoster call (and, on the same tick, one
// FetchApprovals call — the plan's "poll on the existing roster cadence",
// not a separate ticker). On success FetchRoster feeds the bar and resets
// the consecutive-failure streak. On failure it keeps the last-good
// snapshot (documented intent, unchanged) but counts the streak and warns
// exactly once when it reaches rosterFailWarnThreshold — silence
// beyond that point used to make a permanently broken coordinator connection
// indistinguishable from a stable roster. FetchApprovals has no error return
// (PendingApprovals is an in-process call, not a dial) so it has no failure
// path to count.
func (c *Controller) rosterFetch() {
	roster, err := c.opts.FetchRoster()
	if err != nil {
		c.rosterFails++
		if c.rosterFails == rosterFailWarnThreshold {
			c.warn("roster poll: %d consecutive failures, most recent: %v", c.rosterFails, err)
		}
	} else {
		c.rosterFails = 0
		c.sur.SetRoster(roster)
	}
	if c.opts.FetchApprovals != nil {
		c.sur.SetApprovals(c.opts.FetchApprovals())
	}
}
