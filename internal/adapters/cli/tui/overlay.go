package tui

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
)

// Overlay implements termui.Overlay over a bubbletea program. One instance
// per engagement (termui.OverlayFactory builds them).
type Overlay struct {
	ctx    context.Context
	src    Sources
	prefix byte
	start  termui.OverlayStart

	mu      sync.Mutex
	prog    progQuitter
	aborted bool
	// early holds what arrived before the program could take it (a resize,
	// the end of arming, a notice); Run delivers it, in order, once the
	// program exists.
	early []tea.Msg
}

// progQuitter is the running program's quit handle. *tea.Program satisfies it;
// it exists so a test can substitute a stand-in that blocks inside Quit, which
// is the case Abort has to survive — Program.Quit sends onto an UNBUFFERED
// channel and blocks until the event loop drains it, which it cannot do before
// Run reaches that loop or after it has left it.
type progQuitter interface {
	Quit()
	Send(tea.Msg)
}

// NewOverlay builds one engagement's overlay. ctx bounds the feed watches
// (the run's context, so an exiting run releases them). start is how termui
// opened it: by the prefix, or summoned for an approval.
func NewOverlay(ctx context.Context, src Sources, prefix byte, start termui.OverlayStart) *Overlay {
	return &Overlay{ctx: ctx, src: src, prefix: prefix, start: start}
}

// Run drives the overlay to completion. input is the interceptor-routed
// keystroke stream (a pipe, never the tty — bubbletea therefore skips its
// own raw-mode handling); tty is the raw terminal writer, on a screen the
// controller has already taken over (termui's takeScreen). The quick panel
// draws in the bottom PanelRows rows; prefix-then-f draws on the alt screen
// (tea leaves it on quit), unless the engine is itself on it. A summoned
// overlay is the approvals modal: the whole drawable screen from its first
// frame, on the screen termui took for it (its takeover leaves the cursor
// home, so nothing is written before that frame — the frame is what starts
// termui's arming).
func (o *Overlay) Run(input io.Reader, tty io.Writer, geo termui.OverlayGeometry) error {
	watchCtx, cancel := context.WithCancel(o.ctx)
	defer cancel()
	m := NewModel(watchCtx, o.src, geo, o.prefix)
	height := geo.PanelRows
	if o.start.Summoned {
		m, _ = m.openApprovals(true)
		height = geo.Rows
	} else if _, err := io.WriteString(tty, "\x1b["+strconv.Itoa(geo.Rows-geo.PanelRows+1)+";1H"); err != nil {
		// Park the cursor at the panel's top-left: the standard renderer
		// paints downward from where it starts, and the controller
		// cleared/held everything beneath.
		return fmt.Errorf("position overlay: %w", err)
	}
	p := tea.NewProgram(m,
		tea.WithInput(input),
		tea.WithOutput(onlcr{tty}),
		tea.WithoutSignalHandler(),
		// The size has to be stated, and it is the PANEL's, not the terminal's.
		// Input is the interceptor's pipe rather than the tty, so bubbletea v2
		// has nothing to measure and would render into a zero-width screen.
		// Handing it the full terminal height is worse than nothing: v2's
		// renderer then believes it owns all Rows rows and erases to end of
		// screen on every frame, wiping the engine's output above the panel —
		// which is the composition this overlay exists inside.
		// A summoned modal owns every drawable row, so it is sized to them.
		tea.WithWindowSize(geo.Cols, height),
	)
	o.mu.Lock()
	if o.aborted {
		o.mu.Unlock()
		return nil
	}
	o.prog = p
	early := o.early
	o.early = nil
	o.mu.Unlock()
	if len(early) > 0 {
		go func() {
			for _, msg := range early {
				p.Send(msg)
			}
		}()
	}
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

// Resize relays the overlay out for a new terminal size (termui calls it on
// every resize while engaged). Send blocks until the event loop takes the
// message, so it runs off the caller's goroutine — the resize translator's —
// which must never wait on a viewer.
func (o *Overlay) Resize(geo termui.OverlayGeometry) { o.deliver(geometryMsg(geo)) }

// Armed ends a summoned modal's inert window: the model shows the modal as
// live and how many keys the window discarded.
func (o *Overlay) Armed(discarded int) { o.deliver(armedMsg(discarded)) }

// Notify shows an approval that asked for the screen while this overlay held
// it, as a banner; focus stays where the human has it.
func (o *Overlay) Notify(n termui.Notice) { o.deliver(noticeMsg(n.Text)) }

// deliver hands msg to the running program, or keeps it for Run when the
// program does not exist yet. Send blocks until the event loop takes the
// message, so it runs off the caller's goroutine — termui's, which must never
// wait on a viewer.
func (o *Overlay) deliver(msg tea.Msg) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.prog == nil {
		o.early = append(o.early, msg)
		return
	}
	go o.prog.Send(msg)
}

// onlcr is the writer bubbletea draws through: the output post-processing
// bubbletea assumes and the terminal does not do, behind a type that hides
// everything but Write.
//
// bubbletea separates rows with a bare "\n" and moves its cursor model to
// column 0 after one, because with its input not a tty it concludes the
// terminal is still cooked and maps NL to CR-NL itself (ONLCR; tea.go's
// mapNl). ctxloom's terminal is raw — OPOST is off — so nothing returns the
// carriage: every row starts where the previous one ended, and the renderer's
// cursor model no longer matches the screen for any later diffed frame. The
// translation is done here, where bubbletea's output meets the tty.
//
// It matters that this is opaque. Given an output it can recognise as a real
// terminal (an *os.File on a tty), bubbletea v2 takes the terminal over: it
// sets modes and queries capabilities, then waits for the replies — which
// arrive on ITS input. The overlay's input is the interceptor's keystroke
// pipe, never the tty, so those replies never come and the renderer paints
// erase-to-end-of-screen forever without ever writing its content.
//
// ctxloom owns this terminal. The controller has already taken it over,
// saved the engine's cursor and parked it at the panel's top-left, and it
// holds the engine's output for the duration; bubbletea is a guest painting
// into rows it was handed. Passing an opaque writer is what says so — the
// same reasoning the input side states above, applied to output.
type onlcr struct{ w io.Writer }

func (o onlcr) Write(p []byte) (int, error) {
	if bytes.IndexByte(p, '\n') < 0 {
		return o.w.Write(p)
	}
	if _, err := o.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n"))); err != nil {
		return 0, err
	}
	return len(p), nil
}
