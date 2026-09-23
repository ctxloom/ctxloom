package tmuxhost

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// ErrWakeHeld is a typed wake that declined to type: the input line held a
// draft, or an input detector was not quiet. Nothing was written; the mail
// rides the human's next Enter.
var ErrWakeHeld = errors.New("tmuxhost: wake held")

// InputDetector is one route a human's keys can take into the pane. Quiet
// reports whether that route is idle enough for a wake to type; when it is
// not, reason says why.
type InputDetector interface {
	Quiet(ctx context.Context) (ok bool, reason string)
}

// typedWake is the ONE writer that types a wake into a pane.
type typedWake struct {
	host      *PaneHost
	harp      string
	composer  engine.ComposerProbe
	detectors []InputDetector
}

// NewTypedWake binds a wake that types spool.WakeText into harp's pane.
// composer is the engine's probe of its own input line; detectors are the
// host's input routes.
func NewTypedWake(host *PaneHost, harp string, composer engine.ComposerProbe, detectors ...InputDetector) engine.Wake {
	return &typedWake{host: host, harp: harp, composer: composer, detectors: detectors}
}

// Fire checks, in order, that the input line is empty and that every
// detector is quiet — any "no" returns ErrWakeHeld and writes nothing — then
// types the text and presses Enter as TWO send-keys calls. The whole sequence
// holds the pane's write lock, so a human keystroke routed through Input
// cannot land between the check and the typing.
func (w *typedWake) Fire(ctx context.Context, nonce string) error {
	p, err := w.host.pane(w.harp)
	if err != nil {
		return err
	}
	run := w.host.terms.runner
	win := p.term.window

	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	line, err := cursorLine(ctx, run, win)
	if err != nil {
		return fmt.Errorf("typed wake: reading %q's input line: %w", w.harp, err)
	}
	if !w.composer(line) {
		return fmt.Errorf("%w: %q's input line holds a draft", ErrWakeHeld, w.harp)
	}
	for _, d := range w.detectors {
		if ok, reason := d.Quiet(ctx); !ok {
			return fmt.Errorf("%w: %s", ErrWakeHeld, reason)
		}
	}
	// -l types the text literally: a wake text that happened to spell a
	// tmux key name must stay characters.
	if _, err := run.Run(ctx, "send-keys", "-t", win, "-l", spool.WakeText(nonce)); err != nil {
		return fmt.Errorf("typed wake: typing into %q: %w", w.harp, err)
	}
	if _, err := run.Run(ctx, "send-keys", "-t", win, "Enter"); err != nil {
		return fmt.Errorf("typed wake: submitting in %q: %w", w.harp, err)
	}
	return nil
}

// cursorLine returns the pane's line under the cursor, up to the cursor.
func cursorLine(ctx context.Context, run Runner, win string) (string, error) {
	pos, err := run.Run(ctx, "display-message", "-p", "-t", win, "#{cursor_x} #{cursor_y}")
	if err != nil {
		return "", err
	}
	xs, ys, ok := strings.Cut(strings.TrimSpace(pos), " ")
	if !ok {
		return "", fmt.Errorf("unexpected cursor position %q", pos)
	}
	x, errX := strconv.Atoi(xs)
	y, errY := strconv.Atoi(ys)
	if errX != nil || errY != nil {
		return "", fmt.Errorf("unexpected cursor position %q", pos)
	}
	screen, err := run.Run(ctx, "capture-pane", "-p", "-t", win)
	if err != nil {
		return "", err
	}
	lines := strings.Split(screen, "\n")
	if y >= len(lines) {
		return "", nil
	}
	runes := []rune(lines[y])
	if x < len(runes) {
		runes = runes[:x]
	}
	return string(runes), nil
}
