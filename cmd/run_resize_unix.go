//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"golang.org/x/term"
)

// watchResize emits the terminal size now and on every SIGWINCH, for the client
// to pump over the bidi Run stream so the controller resizes the agent's pty.
// The goroutine stops when ctx is done.
func watchResize(ctx context.Context, f *os.File) <-chan *pb.WindowSize {
	ch := make(chan *pb.WindowSize, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)

	emit := func() {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil {
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
	}
	emit() // initial size so the pty matches the terminal immediately

	go func() {
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
