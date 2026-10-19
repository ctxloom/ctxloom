package testenv

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// Display is the session's terminal as the human sees it: everything the
// process wrote, replayed onto a terminal of the pty's size, one line per
// row, trailing blanks trimmed. A full-screen renderer (the split view's)
// draws by cursor address and redraws only the cells that changed, so what
// a human plainly sees need never be contiguous in the bytes; a wait on it
// reads this, never the stream.
func (s *PTYSession) Display() string { return displayOf(s.Output(), s.cols, s.rows) }

// leaveAltScreen is how a full-screen program gives the terminal back.
const leaveAltScreen = "\x1b[?1049l"

// LastFullScreen is the display as a full-screen program (the split view)
// last drew it, before it gave the terminal back: what the human saw last
// while the session ran. With no full-screen exit it is Display.
func (s *PTYSession) LastFullScreen() string {
	out := s.Output()
	if i := strings.LastIndex(out, leaveAltScreen); i >= 0 {
		out = out[:i]
	}
	return displayOf(out, s.cols, s.rows)
}

// displayOf replays out on a cols×rows terminal.
func displayOf(out string, cols, rows int) string {
	screen := vtemu.New(rows, cols)
	screen.Feed([]byte(out))
	lines := make([]string, screen.Rows())
	for i := range lines {
		lines[i] = screen.Row(i)
	}
	return strings.Join(lines, "\n")
}

// AwaitDisplay re-checks cond against the display (Display) on every write
// the pty delivers, until it holds, the process exits (one last look at the
// settled display first), or testsupport.Expiry(t) fires. It returns the
// display it decided on, so a failure can show the screen it waited for.
func (s *PTYSession) AwaitDisplay(t *testing.T, cond func(display string) bool) (string, bool) {
	var display string
	_, verdict, _ := s.awaitScreen(testsupport.Expiry(t), func(out string) bool {
		display = displayOf(out, s.cols, s.rows)
		return cond(display)
	}, nil)
	return display, verdict == screenWanted
}
