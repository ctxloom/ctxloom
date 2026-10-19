package testenv

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDisplayOf_IsTheScreenNotTheStream: the display is what a cols×rows
// terminal shows — text drawn by cursor address lands where it is
// addressed, a cell redrawn shows its last write, and leaving the alternate
// screen gives back the main one.
func TestDisplayOf_IsTheScreenNotTheStream(t *testing.T) {
	stream := "before\r\n" + // the main screen
		"\x1b[?1049h\x1b[2;3Hpane" + // the alternate screen, row 2 column 3
		"\x1b[2;3Hfoo" // a redraw over the same cells
	assert.Equal(t, "\n  fooe\n", displayOf(stream, 10, 3))
	assert.Equal(t, "before\n\n", displayOf(stream+"\x1b[?1049l", 10, 3))
}

// TestLastFullScreen_IsTheScreenBeforeTheTerminalWasGivenBack: once the
// alternate screen is left, the display is the main screen again, and
// LastFullScreen is what the full-screen program last drew.
func TestLastFullScreen_IsTheScreenBeforeTheTerminalWasGivenBack(t *testing.T) {
	s := &PTYSession{out: &ptyCapture{}, cols: 10, rows: 2}
	_, _ = s.out.Write([]byte("shell\r\n\x1b[?1049h\x1b[Hdrawn" + leaveAltScreen + "after"))
	assert.Equal(t, "shell\nafter", s.Display())
	assert.Equal(t, "drawn\n", s.LastFullScreen())
}

// LastFullScreen reads the whole capture when nothing left the alternate
// screen, and nothing drawn before a leave that opens the capture.
func TestLastFullScreen_WithoutALeaveIsTheDisplay(t *testing.T) {
	s := &PTYSession{out: &ptyCapture{}, cols: 10, rows: 2}
	_, _ = s.out.Write([]byte("\x1b[?1049h\x1b[Hdrawn"))
	assert.Equal(t, "drawn\n", s.LastFullScreen())

	opened := &PTYSession{out: &ptyCapture{}, cols: 10, rows: 2}
	_, _ = opened.out.Write([]byte(leaveAltScreen + "after"))
	assert.Equal(t, "\n", opened.LastFullScreen())
}

// AwaitDisplay hands cond the display, returns the one it decided on, and
// fails at once, with the settled display, for a process that has exited.
func TestAwaitDisplay_DecidesOnTheDisplayAndStopsAtTheExit(t *testing.T) {
	s := &PTYSession{out: &ptyCapture{}, cols: 10, rows: 2, exited: make(chan struct{})}
	_, _ = s.out.Write([]byte("\x1b[2;1Hlast"))
	var seen []string
	display, ok := s.AwaitDisplay(t, func(display string) bool {
		seen = append(seen, display)
		return display == "\nlast"
	})
	assert.True(t, ok)
	assert.Equal(t, "\nlast", display)
	assert.Equal(t, []string{"\nlast"}, seen)

	close(s.exited)
	display, ok = s.AwaitDisplay(t, func(string) bool { return false })
	assert.False(t, ok, "an exited process shows nothing more")
	assert.Equal(t, "\nlast", display)
}
