package cmd

import (
	"context"
	"io"
	"os"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"golang.org/x/term"
)

// interactiveTerminal makes the frontend the terminal owner for an interactive
// run: it puts the real terminal in raw mode (so keystrokes pass through
// untouched to the agent's pty) and returns os.Stdin as the keystroke source
// plus a resize channel fed from the terminal size, to pump over the bidi Run
// stream. The returned restore func undoes raw mode. When stdin is not a
// terminal it returns (nil, nil, no-op) and the run proceeds without a pty owner.
func interactiveTerminal(ctx context.Context) (io.Reader, <-chan *pb.WindowSize, func()) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, nil, func() {}
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, nil, func() {}
	}
	return os.Stdin, watchResize(ctx, os.Stdin), func() { _ = term.Restore(fd, oldState) }
}
