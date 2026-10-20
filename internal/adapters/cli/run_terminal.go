package cli

import (
	"context"
	"io"
	"os"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
	"golang.org/x/term"
)

// termIsTerminal / termMakeRaw / termRestore are the seams tests override to
// exercise the terminal-acquisition and -release paths, which cannot be
// provoked against a real terminal from a test. Production points them at
// x/term; the technique is the one run.go documents for execCommand.
// termModeResetSink is where the restore writes terminalModeReset: the
// terminal the engine painted on (stdout), or nil when stdout is not one — the
// engine's mode switches then never reached a terminal and there is nothing to
// undo. termStderr is the terminal ctxloom's own lines go to (stderr).
var (
	termIsTerminal    = term.IsTerminal
	termMakeRaw       = term.MakeRaw
	termRestore       = term.Restore
	termModeResetSink = func() io.Writer {
		if termIsTerminal(int(os.Stdout.Fd())) {
			return os.Stdout
		}
		return nil
	}
	termStderr = func() io.Writer { return os.Stderr }
)

// terminalModeReset turns off the terminal modes an engine session switches on
// for itself and does not reliably switch off — it is killed, it crashes, or
// its own teardown lands after the drive stopped copying its bytes. Each is
// terminal state, not tty state: term.Restore (termios) cannot undo any of
// them, and a mode left on keeps the terminal reporting into the user's shell
// after ctxloom exits. Focus reporting (DECSET 1004) is the visible one: every
// window focus change types ESC[I / ESC[O at the prompt, echoed as ^[[I^[[O.
// Every entry is a DECRST of a mode that is off by default, plus showing the
// cursor (on by default), so resetting a mode the engine never set is a no-op.
// The alternate screen is deliberately absent: leaving it (1049l) restores a
// saved cursor and is not a no-op when the screen was never switched.
const terminalModeReset = "" +
	"\x1b[?1004l" + // focus in/out reporting
	"\x1b[?2004l" + // bracketed paste
	"\x1b[?1000l" + // mouse: button press/release
	"\x1b[?1002l" + // mouse: button-event (drag) tracking
	"\x1b[?1003l" + // mouse: any-event tracking
	"\x1b[?1005l" + // mouse: UTF-8 coordinates
	"\x1b[?1006l" + // mouse: SGR coordinates
	"\x1b[?1015l" + // mouse: urxvt coordinates
	"\x1b[?25h" // cursor visible

// interactiveTerminal makes the frontend the terminal owner for an interactive
// run: it puts the real terminal in raw mode (so keystrokes pass through
// untouched to the runner's pty) and returns os.Stdin as the keystroke source
// plus a resize channel fed from the terminal size, to pump onto the pty
// master. The returned restore func switches off the terminal modes an engine
// may have left on (terminalModeReset) and undoes raw mode; it is idempotent, so
// callers should defer it immediately (panic safety) and may also call it
// inline to put the terminal back before any normal-path output. When stdin is
// not a terminal it returns (nil, nil, no-op) and the run proceeds without a
// pty owner. While the terminal is raw, clidiag's warnings reach it through
// tui.RawLines, so each ends CR LF; the restore hands them back.
func interactiveTerminal(ctx context.Context) (io.Reader, <-chan *agent.WindowSize, func()) {
	fd := int(os.Stdin.Fd())
	if !termIsTerminal(fd) {
		return nil, nil, func() {}
	}
	oldState, err := termMakeRaw(fd)
	if err != nil {
		// Not the same situation as "stdin is not a terminal", though the
		// return value is: stdin IS the user's terminal and it would not hand
		// over raw mode, so the session below runs with no keystroke source at
		// all. Silence here presents as an agent that ignores everything typed
		// at it, with nothing anywhere naming the cause.
		clidiag.Warn("ctxloom", "could not put the terminal in raw mode: %v — this session will not receive your keystrokes", err)
		return nil, nil, func() {}
	}
	restoreSink := clidiag.SetSink(tui.RawLines(termStderr()))
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if w := termModeResetSink(); w != nil {
				_, _ = io.WriteString(w, terminalModeReset)
			}
			if rerr := termRestore(fd, oldState); rerr != nil {
				clidiag.Warn("ctxloom", "failed to restore terminal state: %v", rerr)
			}
			restoreSink()
		})
	}
	return os.Stdin, watchResize(ctx, os.Stdin), restore
}
