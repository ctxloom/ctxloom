package tui

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ctxloom/ctxloom/internal/termui"
)

// Overlay implements termui.Overlay over a bubbletea program. One instance
// per engagement (termui.OverlayFactory builds them).
type Overlay struct {
	ctx    context.Context
	src    Sources
	prefix byte

	mu      sync.Mutex
	prog    *tea.Program
	aborted bool
}

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
	m := NewModel(watchCtx, o.src, geo, o.prefix, tty)
	// Park the cursor at the panel's top-left: the standard renderer paints
	// downward from where it starts, and the controller cleared/held
	// everything beneath.
	if _, err := io.WriteString(tty, "\x1b["+strconv.Itoa(geo.Rows-geo.PanelRows+1)+";1H"); err != nil {
		return fmt.Errorf("position overlay: %w", err)
	}
	p := tea.NewProgram(m,
		tea.WithInput(input),
		tea.WithOutput(tty),
		tea.WithoutSignalHandler(),
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
func (o *Overlay) Abort() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.aborted = true
	if o.prog != nil {
		o.prog.Quit()
	}
}
