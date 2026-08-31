package tui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/termui"
)

// Overlay implements termui.Overlay over a bubbletea program. One instance
// per engagement (termui.OverlayFactory builds them).
type Overlay struct {
	ctx    context.Context
	src    Sources
	prefix byte

	mu      sync.Mutex
	prog    progQuitter
	aborted bool
}

// progQuitter is the running program's quit handle. *tea.Program satisfies it;
// it exists so a test can substitute a stand-in that blocks inside Quit, which
// is the case Abort has to survive — Program.Quit sends onto an UNBUFFERED
// channel and blocks until the event loop drains it, which it cannot do before
// Run reaches that loop or after it has left it.
type progQuitter interface{ Quit() }

// NewOverlay builds one engagement's overlay. ctx bounds the feed watches
// (the run's context, so an exiting run releases them).
func NewOverlay(ctx context.Context, src Sources, prefix byte) *Overlay {
	return &Overlay{ctx: ctx, src: src, prefix: prefix}
}

// Run drives the overlay to completion. input is the interceptor-routed
// keystroke stream (a pipe, never the tty — bubbletea therefore skips its
// own raw-mode handling); tty is the raw terminal writer. The quick panel
// draws over the bottom PanelRows rows; prefix-then-f enters the alt screen
// (tea restores it on quit).
func (o *Overlay) Run(input io.Reader, tty io.Writer, geo termui.OverlayGeometry) error {
	watchCtx, cancel := context.WithCancel(o.ctx)
	defer cancel()
	m := NewModel(watchCtx, o.src, geo, o.prefix)
	// Park the cursor at the panel's top-left: the standard renderer paints
	// downward from where it starts, and the controller cleared/held
	// everything beneath.
	if _, err := io.WriteString(tty, "\x1b["+strconv.Itoa(geo.Rows-geo.PanelRows+1)+";1H"); err != nil {
		return fmt.Errorf("position overlay: %w", err)
	}
	p := tea.NewProgram(m,
		tea.WithInput(input),
		tea.WithOutput(writerOnly{tty}),
		tea.WithoutSignalHandler(),
		// The size has to be stated, and it is the PANEL's, not the terminal's.
		// Input is the interceptor's pipe rather than the tty, so bubbletea v2
		// has nothing to measure and would render into a zero-width screen.
		// Handing it the full terminal height is worse than nothing: v2's
		// renderer then believes it owns all Rows rows and erases to end of
		// screen on every frame, wiping the engine's output above the panel —
		// which is the composition this overlay exists inside.
		tea.WithWindowSize(geo.Cols, geo.PanelRows),
	)
	o.mu.Lock()
	if o.aborted {
		o.mu.Unlock()
		return nil
	}
	o.prog = p
	o.mu.Unlock()
	_, err := p.Run()
	o.mu.Lock()
	o.prog = nil
	o.mu.Unlock()
	return err
}

// Abort asks a running overlay to exit (the interceptor's double-press
// literal path, or the controller closing). Safe before/after Run.
//
// Quit may block, so it is called with the lock RELEASED. Holding it there
// deadlocks the overlay against itself: Run takes the same lock to clear prog
// once p.Run returns, and a p.Run that returns without draining its message
// channel — a failure before the event loop starts — leaves Abort blocked on
// the send and Run blocked on the lock, with the engine's terminal never
// restored.
func (o *Overlay) Abort() {
	o.mu.Lock()
	o.aborted = true
	prog := o.prog
	o.mu.Unlock()
	if prog != nil {
		prog.Quit()
	}
}

// writerOnly hides everything except Write from bubbletea.
//
// It matters that this is opaque. Given an output it can recognise as a real
// terminal (an *os.File on a tty), bubbletea v2 takes the terminal over: it
// sets modes and queries capabilities, then waits for the replies — which
// arrive on ITS input. The overlay's input is the interceptor's keystroke
// pipe, never the tty, so those replies never come and the renderer paints
// erase-to-end-of-screen forever without ever writing its content.
//
// ctxloom owns this terminal. The controller has already set the scroll
// region, saved the cursor and parked it at the panel's top-left, and it holds
// the engine's output for the duration; bubbletea is a guest painting into
// rows it was handed. Passing an opaque writer is what says so — the same
// reasoning the input side states above, applied to output.
type writerOnly struct{ io.Writer }
