// Package ptyrunner provides cross-platform PTY support for running interactive commands.
package ptyrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"

	"github.com/aymanbagabas/go-pty"

	"github.com/ctxloom/shared/agent"
)

// Result contains the exit code from running a command. The session's output
// is NOT captured here: an interactive TUI redraws constantly for hours, so
// buffering the whole stream would grow without bound — callers that want the
// output pass a stdout writer and own the retention policy.
type Result struct {
	ExitCode int
}

// RunInteractive runs a command in interactive mode using a PTY. The PTY makes
// the child see a real terminal even when its stdin is a pipe.
//
// The frontend owns the terminal: raw mode, reading keystrokes, and SIGWINCH all
// happen there, arriving here over the bidi Run stream as the injected stdin
// reader and resize channel. This runner copies stdin into the pty, applies
// resize events, and streams the pty's output to stdout — it never touches the
// controller's own os.Stdin/os.Stdout, so it works for a remote controller.
// stdin and resize may be nil for a non-tty caller.
func RunInteractive(ctx context.Context, cmd *exec.Cmd, stdin io.Reader, stdout, stderr io.Writer, resize <-chan agent.WindowSize) (*Result, error) {
	// Create PTY (cross-platform: Unix PTY or Windows ConPTY)
	ptty, err := pty.New()
	if err != nil {
		return nil, fmt.Errorf("failed to create pty: %w", err)
	}
	defer func() { _ = ptty.Close() }()

	// Create command using PTY
	c := ptty.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env

	// Platform-specific command adjustments (e.g., Windows .cmd/.bat handling)
	adjustPtyCommand(c, cmd)

	// Signal goroutines to stop once the command finishes.
	done := make(chan struct{})
	defer close(done)

	// Start command on PTY slave
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Apply terminal resizes pushed from the frontend over the wire.
	if resize != nil {
		go func() {
			for {
				select {
				case <-done:
					return
				case ws, ok := <-resize:
					if !ok {
						return
					}
					_ = ptty.Resize(int(ws.Cols), int(ws.Rows))
				}
			}
		}()
	}

	// Copy frontend stdin into the PTY. The reader is the wire stdin (an io.Pipe
	// fed by the server's stream pump), so unlike a real os.Stdin it unblocks
	// when the pipe is closed at end of run — no parked-goroutine concern.
	if stdin != nil {
		go func() {
			// When this copier stops reading, unblock the wire's writer: a
			// write into an io.Pipe with no reader parks forever (it is not
			// unblocked by stream/context cancellation), which would wedge the
			// server's stream pump and drop resize messages. Closing the read
			// end makes pending and future writes fail with ErrClosedPipe.
			// Gated to *io.PipeReader so a caller-owned reader (e.g. a real
			// os.Stdin) is never closed from here.
			defer func() {
				if pr, ok := stdin.(*io.PipeReader); ok {
					_ = pr.Close()
				}
			}()
			buf := make([]byte, 1024)
			for {
				n, rerr := stdin.Read(buf)
				if n > 0 {
					select {
					case <-done:
						return
					default:
					}
					if _, werr := ptty.Write(buf[:n]); werr != nil {
						return
					}
				}
				if rerr != nil {
					return
				}
			}
		}()
	}

	// Copy PTY output to the caller's stdout writer (the gRPC stream). The
	// controller does not echo to its own os.Stdout — the frontend renders.
	// With no writer the pty is still drained, or the child would block on a
	// full pty buffer.
	dst := io.Discard
	if stdout != nil {
		dst = stdout
	}
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(dst, ptty)
	}()

	// Wait for command to finish first
	err = c.Wait()

	// Close PTY to unblock the copy goroutine
	// (subprocess MCP servers may still have it open, causing io.Copy to block)
	_ = ptty.Close()

	// Wait for copy to finish
	<-copyDone

	result := &Result{ExitCode: 0}

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else if !isBenignPTYError(err) {
			// Anything that isn't the expected PTY-close fallout is a real
			// failure. Benign close errors are matched by sentinel, not by
			// substring of the error text.
			return nil, fmt.Errorf("command failed: %w", err)
		}
	}

	return result, nil
}
