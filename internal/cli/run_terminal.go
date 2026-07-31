package cli

import (
	"context"
	"io"
	"os"
	"sync"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/vpio"
	"golang.org/x/term"
)

// termIsTerminal / termMakeRaw are the seams tests override to exercise the
// terminal-acquisition failure paths, which cannot be provoked against a real
// terminal from a test. Production points them at x/term; the technique is the
// one run.go documents for execCommand.
var (
	termIsTerminal = term.IsTerminal
	termMakeRaw    = term.MakeRaw
)

// interactiveTerminal makes the frontend the terminal owner for an interactive
// run: it puts the real terminal in raw mode (so keystrokes pass through
// untouched to the agent's pty) and returns os.Stdin as the keystroke source
// plus a resize channel fed from the terminal size, to pump over the bidi Run
// stream. The returned restore func undoes raw mode; it is idempotent, so
// callers should defer it immediately (panic safety) and may also call it
// inline to put the terminal back before any normal-path output. When stdin is
// not a terminal it returns (nil, nil, no-op) and the run proceeds without a
// pty owner.
func interactiveTerminal(ctx context.Context) (io.Reader, <-chan *pb.WindowSize, func()) {
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
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if rerr := term.Restore(fd, oldState); rerr != nil {
				clidiag.Warn("ctxloom", "failed to restore terminal state: %v", rerr)
			}
		})
	}
	return os.Stdin, watchResize(ctx, os.Stdin), restore
}

// pumpResize is the above-the-seam half of SIGWINCH→Resize plumbing: it
// ranges over a terminal-size channel (from watchResize, optionally rewired
// through the termui surround) and relays each event onto a vpio.Session's
// Resize method. The below-the-seam half — actually putting the resize on
// the wire — lives entirely inside the vpio.Launcher implementation
// (internal/vpio/goplugin). A nil channel is a no-op: oneshot runs never
// wire resize, matching the pre-extraction `if resize != nil` guard.
func pumpResize(session vpio.Session, resize <-chan *pb.WindowSize) {
	if resize == nil {
		return
	}
	go func() {
		for ws := range resize {
			session.Resize(ws.Rows, ws.Cols)
		}
	}()
}
