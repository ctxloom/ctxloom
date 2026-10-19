// Package termemu is ctxloom's one terminal-emulator seam: the place a child's
// pty byte stream becomes a screen the split agent view can draw beside the
// agent tree, and the place the user's keys, mouse and pastes become bytes for
// that child.
//
// The split view's compositor (cli/tui's Split) is its consumer. The
// interface speaks ultraviolet's types (the layer bubbletea v2 already decodes
// input into and renders through) and plain cell data, never x/vt's own, so
// libghostty-vt can slot in behind it if x/vt stops being good enough.
package termemu

import (
	"image/color"
	"io"

	uv "github.com/charmbracelet/ultraviolet"
)

// Cell is one screen cell as the emulator holds it.
type Cell struct {
	// Content is the grapheme drawn in the cell; "" is a blank cell.
	Content string
	// Width is the number of columns the grapheme occupies. A wide
	// grapheme's trailing columns are reported with Width 0.
	Width int
	// Style is the cell's SGR state, in the render layer's own type.
	Style uv.Style
}

// Screen is a copy of the visible screen at one instant.
type Screen struct {
	Cols, Rows       int
	Cells            [][]Cell // [row][col]
	CursorX, CursorY int
	CursorVisible    bool
	AltScreen        bool
}

// Text returns the screen as rows of text, trailing blanks trimmed, wide
// graphemes written once. It is the form compared against a reference
// terminal's `capture-pane`.
func (s Screen) Text() []string {
	out := make([]string, s.Rows)
	for y, row := range s.Cells {
		b := make([]byte, 0, len(row))
		for _, c := range row {
			switch {
			case c.Width == 0:
				// a wide grapheme's trailing column: written with it
			case c.Content == "":
				b = append(b, ' ')
			default:
				b = append(b, c.Content...)
			}
		}
		end := len(b)
		for end > 0 && b[end-1] == ' ' {
			end--
		}
		out[y] = string(b[:end])
	}
	return out
}

// Frame is what the renderer draws: a screen committed at a frame boundary.
// While the child holds synchronized output (DEC mode 2026) the committed
// frame is the one from the last boundary, never the half-drawn live screen.
//
// A frame is a copy: nothing the child writes afterwards changes it, so a
// renderer may read it on its own goroutine.
type Frame struct {
	// Seq increases by one per committed frame.
	Seq uint64
	// Buf holds the frame's cells, row by row.
	Buf *uv.Buffer
	// Cursor is where the child's cursor stood at the commit, and
	// CursorVisible whether the child was showing it.
	Cursor        uv.Position
	CursorVisible bool
}

// Line is row y as an ANSI-styled string exactly the frame's width in
// columns, its styles closed at the end, so a compositor can place it after
// anything; "" for a row the frame does not have.
func (f Frame) Line(y int) string {
	if f.Buf == nil {
		return ""
	}
	return f.Buf.Line(y).Render()
}

// MouseTracking is the mouse reporting the child has asked its terminal for.
type MouseTracking int

const (
	// MouseOff: no reports; the human's terminal keeps its own selection.
	MouseOff MouseTracking = iota
	// MouseClicks: presses, releases and the wheel (X10 mode 9, mode 1000).
	MouseClicks
	// MouseDrags adds motion while a button is held (mode 1002).
	MouseDrags
	// MouseMotion adds all motion (mode 1003).
	MouseMotion
)

// Callbacks are what the child asks of the terminal beyond its screen. Each
// is optional and runs on the goroutine writing the child's output, inside
// the emulator: it must not call back into the Terminal, and should hand off
// rather than block.
type Callbacks struct {
	// Title is the window title the child set (OSC 0 or OSC 2).
	Title func(title string)
	// Bell is the child ringing the bell (BEL).
	Bell func()
	// Clipboard is the child writing data to the clipboard selection sel
	// (OSC 52: 'c' the clipboard, 'p' the primary selection, ...), decoded.
	// A child asking to READ a selection is never answered.
	Clipboard func(sel byte, data string)
	// Mouse is the child's mouse tracking changing.
	Mouse func(MouseTracking)
}

// Terminal is one emulated pane. Write feeds it the child's output; the input
// methods encode user events and write them, together with the emulator's
// own replies to the child's queries, to the child-input writer the
// implementation was built with.
type Terminal interface {
	io.Writer
	io.Closer

	// Resize changes the emulated screen. The caller resizes the child's pty
	// to the same size.
	Resize(cols, rows int)
	Size() (cols, rows int)

	// Frame returns the last committed frame. Ready receives (without
	// blocking the emulator) whenever a new frame is committed.
	Frame() Frame
	Ready() <-chan struct{}

	// Snapshot copies the live screen, ignoring frame boundaries. Tests and
	// the reference comparison read it; the renderer reads Frame.
	Snapshot() Screen

	// SendKey, SendMouse and Paste encode one user event for the child, in
	// whichever protocol the child has asked for (kitty keyboard flags,
	// SGR mouse, bracketed paste). Mouse coordinates are pane-relative.
	SendKey(KeyPress)
	SendMouse(MouseEvent)
	Paste(text string)

	// SetDefaultColors makes fg and bg the colours the child's default
	// foreground and background stand for: what it is told when it asks
	// (OSC 10 / OSC 11), which is how it picks a light or dark theme. A nil
	// colour leaves that one as it is.
	SetDefaultColors(fg, bg color.Color)
	// Focus tells the child the pane gained (true) or lost focus, when the
	// child asked for focus reports (mode 1004).
	Focus(in bool)
}
