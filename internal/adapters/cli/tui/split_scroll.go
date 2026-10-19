package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Scroll mode reads the engine's history in its pane. What the engine draws
// off the top of its emulated screen goes into the emulator's history, where
// the live pane never shows it again; scroll mode pages back through that
// history and on into the live screen below it.
//
// The prefix and then [ enter it at the bottom; the prefix and then PgUp
// enter it a page back. PgUp/PgDn, Up/Down, g and G move; q or Esc return
// to live, as does anything that takes the keyboard or the pane away: the
// prefix, another agent selected, the approvals view.
//
// While it is on, nothing the human types, pastes or clicks reaches the
// engine, and the engine's output keeps landing. The view is anchored, as
// tmux's copy mode is: arriving output leaves the same lines in the pane.
// The human's terminal reports the mouse only while it is on, so the wheel
// scrolls the pane; native selection is lost for that time alone, and the
// engine's own mouse mode is the terminal's again on the way out.

const (
	// scrollWheelLines is how far one notch of the wheel scrolls.
	scrollWheelLines = 3
	// splitScrollHints are scroll mode's keys, shown while it is on.
	splitScrollHints = "↑↓ pgup pgdn g G move · q live"
)

// paneScroll is scroll mode's state.
type paneScroll struct {
	on bool
	// top is the line at the pane's first row, counted down the history
	// and on into the live frame: 0 is the oldest history line, the frame's
	// History its first row. It stays put as the history grows: that is the
	// anchor.
	top int
}

// enterScroll turns scroll mode on over the engine's terminal, at the
// bottom or a page back, and hands the pane the keyboard to scroll with.
func (s *Split) enterScroll(pageBack bool) {
	if !s.showingTerminal() {
		return
	}
	s.focus = focusPane
	s.scroll = paneScroll{on: true, top: s.frame.History}
	if pageBack {
		s.scrollBy(-s.h)
	}
}

// scrollBy moves the view d lines, between the oldest history line and the
// live screen.
func (s *Split) scrollBy(d int) {
	s.scroll.top = min(max(s.scroll.top+d, 0), s.frame.History)
}

// scrollKey is a key while scrolling: none reaches the engine.
func (s *Split) scrollKey(msg tea.KeyPressMsg) {
	switch msg.String() {
	case "pgup":
		s.scrollBy(-s.h)
	case "pgdown":
		s.scrollBy(s.h)
	case "up":
		s.scrollBy(-1)
	case "down":
		s.scrollBy(1)
	case "g":
		s.scroll.top = 0
	case "G":
		s.scroll.top = s.frame.History
	case "q", "esc":
		s.scroll = paneScroll{}
	}
}

// scrollMouse is a mouse event while scrolling: the wheel over the pane
// scrolls it, and nothing reaches the engine.
func (s *Split) scrollMouse(msg tea.MouseMsg) {
	w, ok := msg.(tea.MouseWheelMsg)
	panelW, _ := s.layout()
	if !ok || w.X <= panelW {
		return
	}
	switch w.Button {
	case tea.MouseWheelUp:
		s.scrollBy(-scrollWheelLines)
	case tea.MouseWheelDown:
		s.scrollBy(scrollWheelLines)
	}
}

// keepScroll ends scroll mode once what it scrolls is gone: the engine's
// pane, with the keyboard, and no approvals view over it. Every way out
// passes here, so none can leave the human's terminal reporting the mouse
// for a scroll that is over.
func (s *Split) keepScroll() {
	if s.scroll.on && (!s.showingTerminal() || s.focus != focusPane || s.apprOpen) {
		s.scroll = paneScroll{}
	}
}

// scrollLines are the pane's h rows at the scroll position: history lines,
// then the live frame's rows below them, the position marked top right.
func (s *Split) scrollLines(w, h int) []string {
	hist := s.frame.History
	top := min(s.scroll.top, hist)
	var back []string
	if top < hist {
		back = s.pane.History(top, min(h, hist-top))
	}
	live := s.terminalLines(w, h)
	blank := strings.Repeat(" ", w)
	out := make([]string, h)
	for y := range out {
		switch v := top + y - hist; {
		case y < len(back):
			out[y] = padCell(back[y], w)
		case v >= 0 && v < len(live):
			out[y] = live[v]
		default:
			out[y] = blank
		}
	}
	if h > 0 {
		out[0] = scrollMarked(out[0], w, hist-top, hist)
	}
	return out
}

// scrollMarked overlays row's right end with the scroll position: lines
// back from live, of the history's length.
func scrollMarked(row string, w, back, of int) string {
	marker := "[" + strconv.Itoa(back) + "/" + strconv.Itoa(of) + "]"
	mw := len(marker)
	if w <= mw {
		return row
	}
	cut := ansi.Truncate(row, w-mw, "")
	return cut + strings.Repeat(" ", w-mw-ansi.StringWidth(cut)) + ansi.ResetStyle + styleSelected.Render(marker)
}
