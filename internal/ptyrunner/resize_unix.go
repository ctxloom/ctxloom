//go:build !windows

package ptyrunner

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/aymanbagabas/go-pty"
	"golang.org/x/term"
)

// startResizeHandler starts a goroutine that handles terminal resize signals (SIGWINCH)
// and returns a function to stop the handler.
func startResizeHandler(ptty pty.Pty) func() {
	resizeCh := make(chan os.Signal, 1)
	signal.Notify(resizeCh, syscall.SIGWINCH)

	done := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-resizeCh:
				resizePty(ptty)
			}
		}
	}()

	// Trigger initial resize
	resizePty(ptty)

	return func() {
		signal.Stop(resizeCh)
		close(done)
		// Join the goroutine: callers (RunInteractive) rely on the handler
		// being fully stopped — and no longer reading os.Stdin — once this
		// returns. Without the join the goroutine could briefly outlive the
		// run. We do not close(resizeCh): signal.Stop already halts delivery,
		// and closing it would let the goroutine busy-spin on the select's
		// receive-from-closed case before it observes done.
		<-finished
	}
}

// resizePty resizes the PTY to match the current terminal size.
func resizePty(ptty pty.Pty) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}

	width, height, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return
	}

	_ = ptty.Resize(width, height)
}
