package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
)

// The interactive surface's wire vocabulary. A real interactive engine is a
// TUI a human types at; the mock's stand-in for that is the smallest thing a
// test can still drive deterministically over a pty: every typed line comes
// back reflected, every terminal resize is reported with the geometry the
// mock actually read, and one sentinel line ends the session — because a tty
// rarely EOFs, a test needs a way to close the turn on purpose.
//
// Keystroke echo itself is the tty line discipline's: the mock leaves its
// terminal in cooked mode, so what is typed appears as it is typed without
// the mock touching it, and the mock's own reflection is the line-level
// InteractiveEchoPrefix reply. The prompt is NOT read here — Runtime.readPrompt
// takes it off the trailing positional, where L1 says the driver puts it. The
// mock writes no vendor session file, deliberately: imitating a vendor's
// private store would couple this fake to a format ctxloom does not own.
const (
	// InteractiveEchoPrefix precedes the reflection of each non-blank line typed
	// at the mock.
	InteractiveEchoPrefix = "mock echo: "
	// InteractiveWinsizePrefix precedes "<rows>x<cols>" each time the mock
	// observes a resize.
	InteractiveWinsizePrefix = "mock winsize: "
	// InteractiveQuit is the typed line that ends the session.
	InteractiveQuit = "quit"
)

// renderInteractive is the interactive surface's wire arm. It answers the
// positional prompt (when one arrived) with the outcome's response, then
// reflects typed lines and reports resizes until InteractiveQuit or EOF on
// stdin. A resize is reported the moment it is observed rather than folded
// into the next echo, so a test that resized the pty can wait for the
// winsize line directly instead of guessing how long a SIGWINCH takes to land.
//
// Every non-blank line read is a prompt submitted, so the session's
// turn_start hooks fire once per such line — before the echo, the way a
// vendor's prompt-submit hook runs before the model sees the prompt. A hook
// that fails is reported on stderr and the session goes on: a TUI does not
// die because a hook did, and the diagnostic is the evidence a test reads.
//
// A line posted to the session's wake socket (mock.EnvWakeSocket, when the
// environment names one) is taken exactly as a typed line: it is the mock's
// own native wake. The socket is listening before the reply is written, so a
// waker that has seen the reply can post.
//
// A nil Stdin means there is nothing to type at: the session ends after the
// reply, the same "no prompt arrived" shape readPrompt gives a nil reader. A
// nil Resize channel simply never fires.
func (r *Runtime) renderInteractive(promptLen int, out Outcome) error {
	w := r.stdout()
	if r.Stdin == nil {
		return echoResponse(w, promptLen, out)
	}
	lines := readLines(r.Stdin)
	stop, err := listenWakes(r.getenv(mock.EnvWakeSocket), lines)
	if err != nil {
		return err
	}
	defer stop()
	if err := echoResponse(w, promptLen, out); err != nil {
		return err
	}
	hooks, err := r.deliveredHooks()
	if err != nil {
		return err
	}
	return r.interact(w, hooks, lines)
}

// interact takes lines and resizes until the session ends.
func (r *Runtime) interact(w io.Writer, hooks wire.UnifiedHooks, lines <-chan readResult) error {
	resize := r.Resize
	for {
		select {
		case ws, ok := <-resize:
			var err error
			if resize, err = onResize(w, resize, ws, ok); err != nil {
				return err
			}
		case res := <-lines:
			if done, err := r.handleLine(w, hooks, res); done || err != nil {
				return err
			}
		}
	}
}

// listenWakes listens on the wake socket at path and hands each line posted
// to it to lines, beside the typed ones; nothing when path is "". stop closes
// the listener, which removes the socket file, and releases every reader.
func listenWakes(path string, lines chan<- readResult) (stop func(), err error) {
	if path == "" {
		return func() {}, nil
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("mock-engine: listening for wakes on %s: %w", path, err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go readWake(conn, lines, done)
		}
	}()
	return func() { close(done); _ = ln.Close() }, nil
}

// readWake hands on each line of one posted connection until it ends or the
// session does.
func readWake(conn net.Conn, lines chan<- readResult, done <-chan struct{}) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		select {
		case lines <- readResult{line: sc.Text() + "\n"}:
		case <-done:
			return
		}
	}
}

// echoResponse prints the reply when a prompt was given.
func echoResponse(w io.Writer, promptLen int, out Outcome) error {
	if promptLen == 0 {
		return nil
	}
	_, err := fmt.Fprintln(w, out.Response)
	return err
}

// readResult is one line the terminal delivered, or the error that ended it.
type readResult struct {
	line string
	err  error
}

// readLines reads the terminal on its own goroutine. ReadString blocks until
// a line arrives, so reading on its own goroutine is what lets a resize be
// reported while nothing is being typed. The channel is buffered by one so
// the reader can deposit its EOF and exit once the peer hangs up after quit;
// a peer that keeps typing after quit parks it, which is the documented tty
// tradeoff (see the vocabulary doc).
func readLines(in io.Reader) chan readResult {
	lines := make(chan readResult, 1)
	go func() {
		br := bufio.NewReader(in)
		for {
			line, err := br.ReadString('\n')
			lines <- readResult{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()
	return lines
}

// onResize reports one resize, returning the source to keep reading: nil
// once it has closed (a closed source is silence, not a spin).
func onResize(w io.Writer, resize <-chan agent.WindowSize, ws agent.WindowSize, ok bool) (<-chan agent.WindowSize, error) {
	if !ok {
		return nil, nil
	}
	_, err := fmt.Fprintf(w, "%s%dx%d\n", InteractiveWinsizePrefix, ws.Rows, ws.Cols)
	return resize, err
}

// handleLine takes one terminal line: quit ends the session; a non-blank
// line fires the turn_start hooks (a failing hook is reported, and the
// session goes on) and is echoed. done is true when the session ends —
// quit, or the terminal closing.
func (r *Runtime) handleLine(w io.Writer, hooks wire.UnifiedHooks, res readResult) (done bool, err error) {
	line := strings.TrimRight(res.line, "\r\n")
	if line == InteractiveQuit {
		return true, nil
	}
	if line != "" {
		if strings.TrimSpace(line) != "" {
			if err := mock.FireHooks(context.Background(), hooks, "turn_start", "", line, r.Res.Cwd, nil); err != nil {
				fmt.Fprintf(r.stderr(), "mock-engine: %v\n", err)
			}
		}
		if _, err := fmt.Fprintf(w, "%s%s\n", InteractiveEchoPrefix, line); err != nil {
			return true, err
		}
	}
	if res.err == nil {
		return false, nil
	}
	if res.err == io.EOF {
		return true, nil
	}
	return true, fmt.Errorf("reading the terminal: %w", res.err)
}

// deliveredHooks reads the hook file the mock kind's hooks surface announced
// on argv (mock.HooksFlag); the zero set when the grammar declares no such
// flag or the session delivered no hooks. Only the mock's own delivered
// shape is read: a vendor personality's native hook file (claude's
// settings.json) is a surface this runtime probes and reports, never one it
// executes — proving that a vendor binary runs what ctxloom wrote is the live
// hook-firing probe's job, not a fake's.
func (r *Runtime) deliveredHooks() (wire.UnifiedHooks, error) {
	if _, declared := r.CLI.LookupFlag(mock.HooksFlag); !declared {
		return wire.UnifiedHooks{}, nil
	}
	file, ok := r.Argv.Value(mock.HooksFlag)
	if !ok || file == "" {
		return wire.UnifiedHooks{}, nil
	}
	return mock.DeliveredHooksFile(file)
}
