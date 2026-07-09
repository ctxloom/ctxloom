//go:build windows

package cli

import (
	"context"
	"os"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"golang.org/x/term"
)

// watchResize on Windows sends only the initial size: ConPTY tracks terminal
// resizes itself, and there is no SIGWINCH. The channel is closed after the one
// send so the client's resize pump finishes cleanly.
func watchResize(_ context.Context, f *os.File) <-chan *pb.WindowSize {
	ch := make(chan *pb.WindowSize, 1)
	if w, h, err := term.GetSize(int(f.Fd())); err == nil {
		ch <- &pb.WindowSize{Rows: uint32(h), Cols: uint32(w)}
	}
	close(ch)
	return ch
}
