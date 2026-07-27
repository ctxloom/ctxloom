// Package ptyrunner provides cross-platform PTY support for running interactive commands.
package ptyrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// initialResizeWait bounds how long RunInteractive waits for the frontend's
// first resize event before starting the child anyway. go-pty allocates a
// pty at its own default winsize (0x0), not the real terminal's — if the
// child paints before ANY resize reaches it, that first paint uses the wrong
// geometry, and because SIGWINCH only fires on an actual size change, a real
// size that happens to coincide with whatever the child assumed never
// self-heals either (see DEFECT lucid-judo). The frontend always sends its
// captured terminal size once, up front, before anything else (see
// run_resize_unix.go's watchResize), but it travels a chain of goroutine
// hops plus a gRPC round trip to get here, so it cannot be assumed to be
// already buffered — hence a wait, not a non-blocking check. The wait is
// bounded rather than indefinite because an interactive run whose stdin is
// not a real terminal (see interactiveTerminal's non-tty branch) legitimately
// never sends a resize at all; that path must degrade to the pre-fix
// default-size behavior instead of hanging.
const initialResizeWait = 300 * time.Millisecond

// ptyDrainGrace bounds drainPTY's wait for the output-copy goroutine to
// drain whatever the child wrote before RunInteractive forces the pty
// master closed. Closing is ALWAYS eventually required to unblock the
// copier — this process keeps its own reference to the pty's slave side
// open for the whole run (go-pty's design: cmd_unix.go wires the child's
// stdio directly to pty.slave without ever closing it here), so the master
// never sees a natural EOF on its own, with or without a wait. This bound
// is only a safety net for the case drainPTY's FIONREAD poll cannot make
// progress on: a genuine hang (an orphaned subprocess — an MCP server the
// child spawned — still holding the pty open) or a platform where the byte
// count probe is unavailable (pendingPTYBytes, prepare_windows.go). The
// common case (the copier already kept pace, or nothing was buffered at
// exit) returns via drainPTY almost immediately — see its doc.
const ptyDrainGrace = 2 * time.Second

// ptyDrainPollInterval paces drainPTY's FIONREAD poll: short enough that
// draining already-buffered-and-available output adds negligible latency,
// long enough not to spin uselessly while the copy goroutine is merely
// waiting its turn to be scheduled under load.
const ptyDrainPollInterval = 2 * time.Millisecond

// drainPTY waits for the copy goroutine to actually drain whatever the
// child already wrote into the pty before RunInteractive forces the master
// closed (deaf-rut S5): forcing ptty.Close() immediately after c.Wait(),
// with zero grace (the historical behavior), raced the as-yet-unscheduled
// copy goroutine — under CPU load the reader may simply not have run yet,
// so the forced close truncated or entirely dropped output the child had
// already fully written and flushed before exiting.
// TestRunInteractive_CapturesOutput reproduced this on essentially every
// run once contended enough. Rather than a blind sleep, this polls the
// pty's actual buffered byte count (pendingPTYBytes) so the common case —
// the copier already kept pace, nothing left buffered — returns with
// ~zero added latency; only a genuine race (output landed right as the
// child exited) or hang burns real wall-clock time, and even then only
// until the copier is next scheduled, bounded by ptyDrainGrace.
func drainPTY(ptty pty.Pty, copyDone <-chan struct{}) {
	deadline := time.Now().Add(ptyDrainGrace)
	for {
		select {
		case <-copyDone:
			return
		default:
		}
		if n, ok := pendingPTYBytes(ptty); ok && n == 0 {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(ptyDrainPollInterval)
	}
}

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

	// Give the pty its real size BEFORE the child exists: see
	// initialResizeWait. ok=false (closed with nothing sent) and the timeout
	// both fall through to Start() at the pty's default size, same as before
	// this wait existed.
	if resize != nil {
		select {
		case ws, ok := <-resize:
			if ok {
				_ = ptty.Resize(int(ws.Cols), int(ws.Rows))
			}
		case <-time.After(initialResizeWait):
		case <-ctx.Done():
		}
	}

	// Start command on PTY slave
	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("failed to start command: %w", err)
	}

	// Apply subsequent terminal resizes (every SIGWINCH after the initial
	// size consumed above) pushed from the frontend over the wire.
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
		// Deterministically unblock the copier's parked stdin.Read when this
		// function returns: a goroutine parked inside Read cannot observe
		// close(done), and neither ptty.Close (which unblocks ptty.Write) nor
		// the goroutine's own deferred Close (which only fires after it returns)
		// can wake it, so absent this the copier outlives RunInteractive until
		// the caller happens to close the pipe's write end. Gated to
		// *io.PipeReader so a caller-owned reader (e.g. a real os.Stdin) is
		// never closed from here; Close is idempotent with the goroutine's own.
		if pr, ok := stdin.(*io.PipeReader); ok {
			defer func() { _ = pr.Close() }()
		}
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
	// U116-F02: io.Copy's error used to be discarded outright (`_, _ =`), so
	// a write failure on dst (the caller's stdout writer — a gRPC stream,
	// which CAN fail mid-run on a broken pipe/connection reset) left
	// RunInteractive reporting the child's exit code as success having
	// delivered nothing after the failure. trackWriter isolates the WRITE
	// side specifically: a read error from ptty is EXPECTED once this
	// function intentionally closes it below (drainPTY + ptty.Close), so
	// io.Copy's own combined return can't distinguish "we hung up on
	// ourselves on purpose" from "the destination failed" — only the write
	// side can.
	tw := &trackWriter{dst: dst}
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(tw, ptty)
	}()

	// Wait for command to finish first
	err = c.Wait()

	// Close PTY to unblock the copy goroutine (subprocess MCP servers may
	// still have it open, causing io.Copy to block) — but only after
	// drainPTY confirms there is nothing left of the child's own output
	// still sitting unread in the pty (see drainPTY's doc for why this
	// matters and why an unconditional immediate close here used to lose
	// output under load).
	drainPTY(ptty, copyDone)
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

	// U116-F02: a write failure delivering the child's output must not be
	// reported as success — the child may have exited 0 having produced
	// output that never reached anyone.
	if werr := tw.err(); werr != nil {
		return nil, fmt.Errorf("interactive session output delivery failed: %w", werr)
	}

	return result, nil
}

// trackWriter wraps a destination io.Writer, recording the first write
// error it observes (io.Copy stops on the first error, so there is at most
// one) without changing Write's own return values. mu guards the field
// against a caller reading it (via err()) from a different goroutine than
// the copy loop that writes it.
type trackWriter struct {
	dst      io.Writer
	mu       sync.Mutex
	firstErr error
}

func (w *trackWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if err != nil {
		w.mu.Lock()
		if w.firstErr == nil {
			w.firstErr = err
		}
		w.mu.Unlock()
	}
	return n, err
}

func (w *trackWriter) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.firstErr
}
