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
	if !term.IsTerminal(fd) {
		return nil, nil, func() {}
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
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
