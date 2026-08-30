// Package ptyrunner provides cross-platform PTY support for running interactive commands.
package ptyrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"sync"
	"syscall"
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
// self-heals either. The frontend always sends its
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
// finish before RunInteractive forces the pty master closed. Once
// releasePTYSlave has handed this process's slave fd back, the copier
// reaches EOF as soon as the child's output is delivered, so the common
// case does not spend this budget at all. It is the safety net for the two
// cases where no EOF is coming: an orphaned subprocess — an MCP server the
// child spawned — that inherited the slave and still holds it open, and a
// platform whose slave this package cannot reach (ConPTY, where drainPTY
// falls back to the byte-count poll).
const ptyDrainGrace = 2 * time.Second

// ptyDrainPollInterval paces drainPTY's FIONREAD poll: short enough that
// draining already-buffered-and-available output adds negligible latency,
// long enough not to spin uselessly while the copy goroutine is merely
// waiting its turn to be scheduled under load.
const ptyDrainPollInterval = 2 * time.Millisecond

// resizePTY applies a window size to the pty master. It is a package variable
// solely so tests can inject the ioctl failure the error handling around it
// must not swallow — there is no other way to make a live pty's TIOCSWINSZ
// fail on demand. Production never reassigns it.
func applyWindowSize(ptty pty.Pty, ws agent.WindowSize) error {
	return ptty.Resize(int(ws.Cols), int(ws.Rows))
}

var resizePTY = applyWindowSize

// closePTY closes the pty master. Like resizePTY it is a package variable only
// so tests can observe and fault-inject the call; production never reassigns it.
func closePTYMaster(ptty pty.Pty) error { return ptty.Close() }

var closePTY = closePTYMaster

// firstError records the first error observed on a side path that has no
// return value of its own, so the owning call can report it before claiming
// success. mu guards the field against a reader on a different goroutine than
// the writer.
type firstError struct {
	mu  sync.Mutex
	err error
}

func (e *firstError) set(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err == nil {
		e.err = err
	}
}

func (e *firstError) get() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// releasePTYSlave closes THIS process's own fd for the pty's slave end and
// reports whether an end-of-file on the master is now reachable.
//
// go-pty wires the child's stdin/stdout/stderr straight to pty.slave and
// keeps that *os.File open for the pty's whole lifetime (cmd_unix.go), so a
// read on the master never ends by itself even after the child is reaped —
// which is the only reason RunInteractive has to force the master closed at
// all. Handing the slave back first turns that forced close into a fallback
// instead of the primary mechanism: with the child gone and this fd
// released, the copier's read drains what the child wrote and THEN ends.
//
// A pty whose slave this package cannot reach (ConPTY on Windows, where the
// type assertion fails) reports false and keeps the byte-count fallback.
// An already-closed slave is success, not failure: the goal is the state,
// not the syscall.
func releasePTYSlave(ptty pty.Pty) bool {
	up, ok := ptty.(pty.UnixPty)
	if !ok {
		return false
	}
	f := up.Slave()
	if f == nil {
		return false
	}
	err := f.Close()
	return err == nil || errors.Is(err, fs.ErrClosed)
}

// drainPTY makes sure the copy goroutine has actually delivered whatever the
// child wrote before RunInteractive forces the master closed. Forcing
// ptty.Close() immediately after c.Wait() with zero grace (the historical
// behavior) raced the as-yet-unscheduled copy goroutine, so under CPU load
// the forced close truncated or entirely dropped output the child had
// already written and flushed before exiting.
//
// It JOINS on the copier rather than sampling how much is left, because the
// byte count cannot answer the question being asked. pendingPTYBytes reports
// the master's line-discipline read queue; data the slave has written but
// that the kernel has not yet pushed out of the tty flip buffer is invisible
// to it. So a zero reading has two meanings — "the copier already took
// everything" and "the child's bytes have not surfaced yet" — and the second
// one is precisely the contended case this drain exists for.
// TestRunInteractive_CapturesOutput failed on exactly that ambiguity: the
// child exited 0, TIOCINQ read 0 while its 21 bytes were still in flight,
// the drain returned in ~50us, and the caller got an empty capture at 0.00s
// elapsed.
//
// The byte-count poll survives only where no slave is reachable to release
// (ConPTY), where it remains better than a blind sleep.
func drainPTY(ptty pty.Pty, copyDone <-chan struct{}) {
	if releasePTYSlave(ptty) {
		select {
		case <-copyDone:
		case <-time.After(ptyDrainGrace):
		}
		return
	}
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

// RunInteractive runs a command in interactive mode using a PTY. The PTY makes
// the child see a real terminal even when its stdin is a pipe. Its only
// return payload is the exit code: the session's output is NOT
// captured here — an interactive TUI redraws constantly for hours, so
// buffering the whole stream would grow without bound — callers that want
// the output pass a stdout writer and own the retention policy. A plain int
// return (rather than a one-field struct behind a pointer) means the error
// path is the only nil-shaped thing a caller ever has to check.
//
// The frontend owns the terminal: raw mode, reading keystrokes, and SIGWINCH all
// happen there, arriving here over the bidi Run stream as the injected stdin
// reader and resize channel. This runner copies stdin into the pty, applies
// resize events, and streams the pty's output to out — it never touches the
// controller's own os.Stdin/os.Stdout, so it works for a remote controller.
// stdin and resize may be nil for a non-tty caller.
//
// out receives the child's ENTIRE output, stdout and stderr interleaved:
// a pty gives the child one stream (fd 1 and fd 2 are both the slave), so
// there is no separation left at the master to route to a second writer.
// A caller that needs the two streams apart must not use a pty — see
// internal/lm/backends' non-interactive branch, which wires cmd.Stdout and
// cmd.Stderr directly.
//
// stdinCleanup releases whatever backs stdin, and is supplied by the layer
// that CREATED that reader, because only that layer knows what closing it
// means. It runs exactly once, when the stdin copier stops reading — which is
// the event the wire side is waiting on: a write into an io.Pipe with no
// reader parks forever and is NOT unblocked by context or stream
// cancellation, so without this the server's stream pump wedges and the
// copier's own goroutine never unparks. It may be nil for a caller whose
// stdin needs no release (a real os.Stdin the caller still owns).
//
// It is an explicit func rather than something inferred from stdin's dynamic
// type on purpose. This cleanup was once gated on `stdin.(*io.PipeReader)`,
// which held right up until the terminal wake began handing the engine a
// WRAPPED reader; the assertion then failed on every run, both closes became
// silent no-ops, and nothing went red because a skipped cleanup looks exactly
// like a clean one. Taking the func from the owner makes that failure
// unrepresentable: this package no longer has an opinion about what stdin is.
func RunInteractive(ctx context.Context, cmd *exec.Cmd, stdin io.Reader, stdinCleanup func(), out io.Writer, resize <-chan agent.WindowSize) (int, error) {
	// Create PTY (cross-platform: Unix PTY or Windows ConPTY)
	ptty, err := pty.New()
	if err != nil {
		return 0, fmt.Errorf("failed to create pty: %w", err)
	}
	// The master is closed exactly once, by whichever path reaches it first:
	// the explicit close after the child exits (which is what unblocks the
	// output copier) or this defer, which covers every early return above it.
	// Both used to call Close directly and drop the result, so correctness
	// rested on go-pty's Close being idempotent — something its API does not
	// promise and which is true today only because the Unix implementation
	// closes *os.File handles that track their own closed state. Once makes
	// that this code's invariant rather than a borrowed one, and leaves a
	// single result to inspect instead of two to discard.
	closeOnce := sync.OnceValue(func() error { return closePTY(ptty) })
	defer func() { _ = closeOnce() }()

	c := ptyCommand(ctx, ptty, cmd)

	// Signal goroutines to stop once the command finishes.
	done := make(chan struct{})
	defer close(done)

	if err := applyInitialSize(ctx, ptty, resize); err != nil {
		return 0, err
	}

	// Start command on PTY slave
	if err := c.Start(); err != nil {
		return 0, fmt.Errorf("failed to start command: %w", err)
	}

	var resizeErr firstError
	startResizeApplier(ptty, resize, done, &resizeErr)

	// Deterministically unblock the stdin copier's parked Read when this
	// function returns: a goroutine parked inside Read cannot observe
	// close(done), and neither ptty.Close (which unblocks ptty.Write) nor the
	// copier's own deferred cleanup (which only fires after it returns) can
	// wake it, so absent this the copier outlives RunInteractive until the
	// caller happens to release the reader itself. sync.OnceFunc makes this
	// safe to race with the copier's identical call — whichever arrives first
	// wins and the other is a no-op — so the owner's func need not be
	// idempotent on its own. This defer must live in RunInteractive rather
	// than the helper: it is RunInteractive's return that has to trigger it.
	releaseStdin := func() {}
	if stdinCleanup != nil {
		releaseStdin = sync.OnceFunc(stdinCleanup)
	}
	defer releaseStdin()
	startStdinCopier(ptty, stdin, releaseStdin, done)

	tw, copyDone := startOutputCopier(ptty, out)

	// Wait for command to finish first
	waitErr := c.Wait()

	// Close PTY to unblock the copy goroutine (subprocess MCP servers may
	// still have it open, causing io.Copy to block) — but only after
	// drainPTY confirms there is nothing left of the child's own output
	// still sitting unread in the pty (see drainPTY's doc for why this
	// matters and why an unconditional immediate close here used to lose
	// output under load).
	drainPTY(ptty, copyDone)
	closeErr := closeOnce()

	// Wait for copy to finish
	<-copyDone

	return runResult(waitErr, closeErr, tw, &resizeErr)
}

// ptyCommand builds go-pty's own Cmd from the caller's *exec.Cmd.
//
// cmd.Args holds argv[0] at index 0. os/exec defaults an empty Args to a
// one-element slice containing Path, but it does so inside exec.Cmd.Start,
// which is never reached from here — the argv is re-derived for go-pty's own
// Cmd instead — so a hand-built &exec.Cmd{Path: ...} arrives with Args nil and
// must not be sliced unconditionally.
//
// argv[0] itself is not ours to set: go-pty's Cmd.start rebuilds the child
// argv as exec.Command(Path, Args[1:]...), so the child always sees Path as
// argv[0] and a caller's own Args[0] cannot reach it through this library.
func ptyCommand(ctx context.Context, ptty pty.Pty, cmd *exec.Cmd) *pty.Cmd {
	var args []string
	if len(cmd.Args) > 1 {
		args = cmd.Args[1:]
	}
	c := ptty.CommandContext(ctx, cmd.Path, args...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	// Platform-specific command adjustments (e.g., Windows .cmd/.bat handling)
	adjustPtyCommand(c, cmd)
	return c
}

// applyInitialSize gives the pty its real size BEFORE the child exists: see
// initialResizeWait. A closed channel and the timeout both fall through to the
// caller's Start at the pty's default size, same as before this wait existed.
//
// A resize FAILURE here is not survivable as a silent one: the whole point of
// the wait is that the child gets its real geometry before its first paint,
// and a size that never lands never self-heals because SIGWINCH only fires on
// a CHANGE. Starting the child anyway hands the user a session painted at the
// pty's default 0x0 with nothing to say why.
func applyInitialSize(ctx context.Context, ptty pty.Pty, resize <-chan agent.WindowSize) error {
	if resize == nil {
		return nil
	}
	select {
	case ws, ok := <-resize:
		if !ok {
			return nil
		}
		if err := resizePTY(ptty, ws); err != nil {
			return fmt.Errorf("failed to size pty before starting the child: %w", err)
		}
	case <-time.After(initialResizeWait):
	case <-ctx.Done():
	}
	return nil
}

// startResizeApplier applies every SIGWINCH after the initial size (consumed
// by applyInitialSize) pushed from the frontend over the wire, recording the
// first reportable failure in sink.
func startResizeApplier(ptty pty.Pty, resize <-chan agent.WindowSize, done <-chan struct{}, sink *firstError) {
	if resize == nil {
		return
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case ws, ok := <-resize:
				if !ok {
					return
				}
				err := resizePTY(ptty, ws)
				if err == nil {
					continue
				}
				// Two distinct failures share this branch. One is the caller's
				// own shutdown racing a queued event: RunInteractive closes the
				// pty before the deferred close(done) that stops this goroutine,
				// so a late resize lands on a closed master — the same expected
				// fallout isBenignPTYError already names for c.Wait, and not a
				// defect. The other is a genuinely broken master, which leaves
				// the child painting at a stale geometry for the rest of the
				// session with nothing to say why. Only the second is
				// reportable; either way the ioctl will keep failing, so stop
				// rather than retry in silence.
				if !isBenignPTYError(err) {
					sink.set(err)
				}
				return
			}
		}
	}()
}

// startStdinCopier copies frontend stdin into the PTY. The reader is the wire
// stdin — an io.Pipe fed by the server's stream pump, possibly behind a
// wrapper — and releaseStdin is the owner's cleanup for it (see
// RunInteractive), already reduced to run exactly once.
func startStdinCopier(ptty pty.Pty, stdin io.Reader, releaseStdin func(), done <-chan struct{}) {
	if stdin == nil {
		return
	}
	go func() {
		// When this copier stops reading, release the reader so the wire's
		// writer unblocks: a write into an io.Pipe with no reader parks
		// forever (it is not unblocked by stream/context cancellation), which
		// would wedge the server's stream pump and drop resize messages.
		// Releasing makes pending and future writes fail with ErrClosedPipe.
		defer releaseStdin()
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

// startOutputCopier copies PTY output to the caller's writer (the gRPC
// stream). The controller does not echo to its own os.Stdout — the frontend
// renders. With no writer the pty is still drained, or the child would block
// on a full pty buffer. The returned channel closes when the copy ends.
//
// io.Copy's error used to be discarded outright, so a write failure on the
// destination (a gRPC stream, which CAN fail mid-run on a broken pipe or
// connection reset) left RunInteractive reporting the child's exit code as
// success having delivered nothing after the failure. trackWriter isolates the
// WRITE side specifically: a read error from ptty is EXPECTED once the caller
// intentionally closes it (drainPTY + close), so io.Copy's own combined return
// cannot distinguish "we hung up on ourselves on purpose" from "the
// destination failed" — only the write side can.
func startOutputCopier(ptty pty.Pty, out io.Writer) (*trackWriter, <-chan struct{}) {
	dst := io.Discard
	if out != nil {
		dst = out
	}
	tw := &trackWriter{dst: dst}
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		_, _ = io.Copy(tw, ptty)
	}()
	return tw, copyDone
}

// signaledStatus is the portable slice of the platform wait status this
// package needs. os/exec exposes it as ProcessState.Sys(), typed
// syscall.WaitStatus — a per-GOOS struct, so asserting the concrete type would
// only build on unix. Both shapes carry these two methods, so an interface
// assertion compiles everywhere and needs no build-tagged file.
type signaledStatus interface {
	Signaled() bool
	Signal() syscall.Signal
}

// ExitStatusFor maps a finished child's *exec.ExitError to a valid POSIX exit
// status. os/exec reports -1 for a process that died on a signal, and -1 is not
// an exit status a process can carry: propagated to os.Exit the OS truncates it
// to 255, making a killed engine indistinguishable from an engine that really
// exited 255 and from a runner-internal failure. Shells have long since settled
// this — a signalled child reports 128+signum (130 SIGINT, 137 SIGKILL, 143
// SIGTERM) — and ctxloom's own launch path is a transparent wrapper around the
// engine's status, so it reports what a shell would.
//
// WINDOWS, stated rather than left implicit: Windows keeps today's behaviour.
// Its syscall.WaitStatus satisfies the interface above but hard-codes
// Signaled() to false and Signal() to -1, because Windows has no wait status
// that distinguishes "killed by a signal" from an ordinary exit code —
// TerminateProcess just sets the exit code. So the assertion succeeds, the
// guard is false, and the ExitCode() fallthrough runs, which is exactly the
// pre-change path. There is nothing to map there; when there is nothing to
// map, the right answer is to say so, not to invent a synthetic signal.
func ExitStatusFor(exitErr *exec.ExitError) int {
	if status, ok := exitErr.Sys().(signaledStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return exitErr.ExitCode()
}

// runResult folds the child's outcome and every side-path failure into the one
// (exit code, error) pair RunInteractive returns. Every branch here exists
// because the corresponding failure was once silent: a run that delivered
// nothing, or resized nothing, must not report the child's exit code as
// success.
func runResult(waitErr, closeErr error, tw *trackWriter, resizeErr *firstError) (int, error) {
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = ExitStatusFor(exitErr)
		} else if !isBenignPTYError(waitErr) {
			// Anything that isn't the expected PTY-close fallout is a real
			// failure. Benign close errors are matched by sentinel, not by
			// substring of the error text.
			return 0, fmt.Errorf("command failed: %w", waitErr)
		}
	}

	// A write failure delivering the child's output must not be reported as
	// success — the child may have exited 0 having produced output that never
	// reached anyone.
	if werr := tw.err(); werr != nil {
		return 0, fmt.Errorf("interactive session output delivery failed: %w", werr)
	}

	// The close that unblocks the copier is a real syscall on the master and
	// can fail; only the fallout of closing a pty whose child has already gone
	// is expected (isBenignPTYError, as for c.Wait above).
	if closeErr != nil && !isBenignPTYError(closeErr) {
		return 0, fmt.Errorf("interactive session pty close failed: %w", closeErr)
	}

	// Same standard for the resize path: a frontend SIGWINCH that stopped
	// reaching the pty leaves the child painting at a geometry the user can
	// see is wrong and cannot correct — SIGWINCH only fires on a change, so
	// resending the same size is a no-op and the session never recovers.
	if rerr := resizeErr.get(); rerr != nil {
		return 0, fmt.Errorf("interactive session terminal resize failed: %w", rerr)
	}

	return exitCode, nil
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
