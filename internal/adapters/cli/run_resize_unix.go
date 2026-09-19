//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"golang.org/x/term"
)

// watchResize emits the terminal size now and on every SIGWINCH, for the client
// to pump over the bidi Run stream so the controller resizes the agent's pty.
// When ctx is done the goroutine unregisters the SIGWINCH handler and closes
// the channel so the client's resize pump finishes cleanly (matching the
// Windows variant's contract) — otherwise long-lived processes that run more
// than once (the init flow) leak a pump goroutine and a signal handler per run.
func watchResize(ctx context.Context, f *os.File) <-chan *pb.WindowSize {
	ch := make(chan *pb.WindowSize, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)

	emit := func() {
		w, h, err := term.GetSize(int(f.Fd()))
		if err != nil {
			// Nothing downstream can tell "no size event" from "the terminal
			// is unchanged", so the pty silently keeps whatever geometry it
			// was created with and the agent renders into the wrong width for
			// the rest of the session. WarnOnce because this closure also runs
			// on every SIGWINCH — a persistently unreadable terminal must not
			// stream the same line into the user's session.
			clidiag.WarnOnce("ctxloom", "could not read the terminal size: %v — the agent's pty keeps its default geometry", err)
			return
		}
		ws := &pb.WindowSize{Rows: uint32(h), Cols: uint32(w)}
		// Latest-wins coalescing: a pending size is STALE, not "as good as
		// the latest" — after a resize burst the pty would settle on an
		// out-of-date size until the next SIGWINCH. Sole producer, so the
		// retry send cannot block.
		select {
		case ch <- ws:
		default:
			select {
			case <-ch:
			default:
			}
			ch <- ws
		}
	}
	emit() // initial size so the pty matches the terminal immediately

	go func() {
		defer close(ch) // sole sender after the initial emit, so this is safe
		defer signal.Stop(sig)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sig:
				emit()
			}
		}
	}()
	return ch
}
