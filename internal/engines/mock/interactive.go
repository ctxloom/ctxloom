package mockengine

import (
	"bufio"
	"fmt"
	"io"
	"strings"
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
// A nil Stdin means there is nothing to type at: the session ends after the
// reply, the same "no prompt arrived" shape readPrompt gives a nil reader. A
// nil Resize channel simply never fires.
func (r *Runtime) renderInteractive(promptLen int, out Outcome) error {
	w := r.stdout()
	if promptLen > 0 {
		if _, err := fmt.Fprintln(w, out.Response); err != nil {
			return err
		}
	}
	if r.Stdin == nil {
		return nil
	}

	// ReadString blocks until a line arrives, so reading on its own goroutine
	// is what lets a resize be reported while nothing is being typed. The
	// channel is buffered by one so the reader can deposit its EOF and exit
	// once the peer hangs up after quit; a peer that keeps typing after quit
	// parks it, which is the documented tty tradeoff (see the vocabulary doc).
	type readResult struct {
		line string
		err  error
	}
	lines := make(chan readResult, 1)
	go func() {
		br := bufio.NewReader(r.Stdin)
		for {
			line, err := br.ReadString('\n')
			lines <- readResult{line: line, err: err}
			if err != nil {
				return
			}
		}
	}()

	resize := r.Resize
	for {
		select {
		case ws, ok := <-resize:
			if !ok {
				resize = nil // a closed source is silence, not a spin
				continue
			}
			if _, err := fmt.Fprintf(w, "%s%dx%d\n", InteractiveWinsizePrefix, ws.Rows, ws.Cols); err != nil {
				return err
			}
		case res := <-lines:
			line := strings.TrimRight(res.line, "\r\n")
			if line == InteractiveQuit {
				return nil
			}
			if line != "" {
				if _, err := fmt.Fprintf(w, "%s%s\n", InteractiveEchoPrefix, line); err != nil {
					return err
				}
			}
			if res.err != nil {
				if res.err == io.EOF {
					return nil
				}
				return fmt.Errorf("reading the terminal: %w", res.err)
			}
		}
	}
}
