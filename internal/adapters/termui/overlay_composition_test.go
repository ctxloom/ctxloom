//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// This file is the F1 termui<->tui COMPOSITION harness (Wave F playbook,
// item 2): controller_test.go (package termui, internal) already proves the
// Controller's engage/hold/replay/nudge/degrade lifecycle against a
// fakeOverlay; what was uncovered is the REAL seam production wires at
// run_terminal_ui.go:77 (NewOverlay: func() termui.Overlay { return
// tui.NewOverlay(ctx, src, prefix, start) }) — the actual bubbletea Program running
// behind the Controller, over a REAL pty (aymanbagabas/go-pty; no tmux/
// teatest/vt10x per the playbook's binding principles). Because tui imports
// termui (roster.go), this file is package termui_test (an external test
// package) — an internal termui test importing tui would cycle.
//
// syncBuf/await deliberately re-derive controller_test.go's
// lockedBuffer/await idioms rather than reuse them: those helpers are
// unexported to package termui's internal tests and unreachable from this
// external package.

const compPrefix byte = 0x1d

// syncBuf is a goroutine-safe io.Writer standing in for the tty side a real
// pty's master-read loop feeds. Every write is an event a wait wakes on.
type syncBuf struct {
	mu      sync.Mutex
	b       strings.Builder
	written chan struct{} // closed by the next write
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written != nil {
		close(s.written)
		s.written = nil
	}
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// next returns what has been written and a channel the next write closes.
func (s *syncBuf) next() (string, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written == nil {
		s.written = make(chan struct{})
	}
	return s.b.String(), s.written
}

// await re-checks cond on every write until it holds, and reports false only
// when the wait runs out.
func (s *syncBuf) await(t waiter, cond func(string) bool) (string, bool) {
	t.Helper()
	expired := testsupport.Expiry(t)
	for {
		cur, written := s.next()
		if cond(cur) {
			return cur, true
		}
		select {
		case <-written:
		case <-expired:
			return s.String(), false
		}
	}
}

// waitUntil blocks until what has arrived satisfies cond.
func (s *syncBuf) waitUntil(t *testing.T, what string, cond func(string) bool) {
	t.Helper()
	if cur, ok := s.await(t, cond); !ok {
		t.Fatalf("never saw %s; arrived: %q", what, cur)
	}
}

// contains is a waitUntil condition.
func contains(sub string) func(string) bool {
	return func(s string) bool { return strings.Contains(s, sub) }
}

// await receives the event ch carries, failing when the wait runs out.
func await[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-testsupport.Expiry(t):
		t.Fatalf("never received %s", what)
		var zero T
		return zero
	}
}

// awaitWatch waits for the overlay to open harp's feed: a Watch call is the
// only frame-diff-proof evidence that it did.
func awaitWatch(t *testing.T, watches <-chan string, harp string) {
	t.Helper()
	for await(t, "a Watch call for "+harp, watches) != harp {
	}
}

// newComposedPTY opens a real Unix pty (go-pty), continuously draining the
// master side's Reads into a syncBuf (what a terminal emulator watching the
// master would see), and returns the master (for the test to write
// "keystrokes" into) and the slave *os.File (wired as the Controller's own
// Stdin/TTY — exactly what os.Stdin/os.Stdout are in a real interactive
// run). Controller.New's own doc says the caller guarantees raw mode is
// already established on the real terminal before wiring it in — production
// does this on the real os.Stdin/os.Stdout before setupTerminalUI
// (run_terminal.go's term.MakeRaw); a freshly opened pty slave defaults to
// cooked/canonical mode with local echo, so this harness does the same
// MakeRaw production relies on, or every keystroke would be line-buffered
// and echoed back onto the "tty" reader instead of reaching the interceptor
// immediately.
func newComposedPTY(t *testing.T) (master pty.Pty, slave *os.File, tty *syncBuf) {
	t.Helper()
	p, err := pty.New()
	require.NoError(t, err)
	up, ok := p.(pty.UnixPty)
	require.True(t, ok, "this harness targets a Unix pty (go-pty's ConPty is Windows-only)")
	slave = up.Slave()
	oldState, err := term.MakeRaw(int(slave.Fd()))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = term.Restore(int(slave.Fd()), oldState)
		_ = slave.Close()
		_ = p.Close()
	})
	tty = &syncBuf{}
	go func() { _, _ = io.Copy(tty, p) }()
	return p, slave, tty
}

// realOverlaySources is a minimal tui.Sources good enough to drive the real
// Model through one auto-opened roster row (tui's own fakeSources is
// unexported and package-local — this re-derives the same idiom externally).
// Every Watch call is sent on the returned channel: that call is the only
// frame-diff-proof evidence that a feed was opened (see the engage assertion).
func realOverlaySources(harp string) (tui.Sources, <-chan string) {
	watches := make(chan string, 16)
	return tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) {
			return []tui.RosterRow{{Harp: harp, State: "live"}}, nil
		},
		Watch: func(_ context.Context, h string) (*tui.Feed, error) {
			watches <- h
			return &tui.Feed{
				Source: "live",
				Events: make(chan operations.SessionFeedEvent),
				Errs:   make(chan error, 1),
				Cancel: func() {},
			}, nil
		},
	}, watches
}

// pumpEngineInput drains c.Stdin() (the interceptor's engine-bound side) —
// the same role controller_test.go's ctlHarness pump goroutine plays,
// standing in for the plugin client's stdin pump.
func pumpEngineInput(c *termui.Controller) *syncBuf {
	engine := &syncBuf{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := c.Stdin().Read(buf)
			if n > 0 {
				_, _ = engine.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return engine
}

// TestOverlayComposition_EngageHoldReplayNudge wires the Controller to the
// REAL tui.Overlay exactly as run_terminal_ui.go:77 does, driven over a real
// pty pair. It re-proves controller_test.go's
// TestController_EngageHoldReplayNudge invariants (prefix engages; engine
// output held during engagement; the atomic release ordering: engine screen →
// scroll region restore → engine cursor restore (DECRC) → held-output
// replay; a post-release resize nudge) against the GENUINE bubbletea Program
// and a GENUINE pty, not the fake.
func TestOverlayComposition_EngageHoldReplayNudge(t *testing.T) {
	ptyDev, slave, tty := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, watches := realOverlaySources("perky-same-chevy")

	resize := make(chan *agent.WindowSize, 4)
	warns := make(chan string, 4)
	c := termui.New(termui.Options{
		Stdin:    slave,
		TTY:      slave,
		Resize:   resize,
		Prefix:   compPrefix,
		Surround: true,
		// Deliberately a DIFFERENT harp than the roster row's below: the bar
		// paints its own identity independently of the overlay, so sharing a
		// harp string would make "the overlay painted" indistinguishable
		// from "the bar painted" in the pty capture.
		Bar: termui.BarInfo{Harp: "self-session", Engine: "claude-code", PrefixHint: "^]"},
		// The exact production wiring (run_terminal_ui.go:77): the overlay
		// factory closes over the real tui.NewOverlay constructor.
		NewOverlay: func(start termui.OverlayStart) termui.Overlay { return tui.NewOverlay(ctx, src, compPrefix, start) },
		Warn:       func(format string, args ...any) { warns <- fmt.Sprintf(format, args...) },
	})
	defer c.Close()
	engine := pumpEngineInput(c)

	resize <- &agent.WindowSize{Rows: 24, Cols: 80}
	ws := await(t, "the initial size", c.Resize())
	require.Equal(t, uint16(23), ws.Rows, "initial size reaches the engine reserved")
	tty.waitUntil(t, "the surround's region", contains("\x1b[1;23r"))

	// Engage: prefix + a viewer key, written to the pty's MASTER side (the
	// "terminal emulator" role) — the controller's interceptor reads them
	// off the slave, exactly as it would read a real terminal's stdin.
	_, err := ptyDev.Write([]byte{compPrefix, 'j'})
	require.NoError(t, err)
	// The real overlay's panel reaches the pty (the composition this test
	// exists for), and the roster row's feed is really opened.
	//
	// The two are asserted SEPARATELY because the old single assertion — the
	// literal "feed: perky-same-chevy" appearing on the pty — is only
	// observable when the roster resolves before the tea.Program's FIRST
	// frame. Lose that race, which is all a busy box has to do, and the
	// overlay's first frame paints the empty panel ("feed: —") while the
	// roster arrives into a DIFFED repaint: bubbletea v2 rewrites only the
	// changed cells, so the harp never appears as a contiguous string on the
	// pty at all. (Measured: with a 30s budget the string still never came.)
	// The same trap is already documented on this file's sibling assertion in
	// tui/overlay_test.go — this one just had not been converted.
	//
	// So: the panel's own frame proves the overlay painted THROUGH the pty
	// (which is the composition claim), and the Watch call proves the row's
	// feed was auto-opened (which is the behaviour claim). Neither depends on
	// which side of the race won.
	tty.waitUntil(t, "the real overlay's panel on the pty", contains("feed:"))
	awaitWatch(t, watches, "perky-same-chevy")
	assert.Contains(t, tty.String(), "\x1b[?1049h\x1b[r",
		"engage moves to the alternate screen (saving the engine's screen and cursor) with the full scroll region")

	// Engine output during engagement is held (never reaches the pty).
	before := tty.String()
	_, _ = c.Stdout().Write([]byte("HELD-OUTPUT"))
	assert.NotContains(t, tty.String(), "HELD-OUTPUT")
	assert.Equal(t, before, tty.String(), "nothing hits the pty while held")

	// Release: 'q' backs the REAL overlay Model out (tea.Quit); the
	// Controller's one atomic restore write + replay + nudge follow.
	_, err = ptyDev.Write([]byte("q"))
	require.NoError(t, err)
	// The nudge is sent after the release wrote its restore and replay to the
	// pty; the pty delivers them to this side in its own time.
	first := await(t, "the repaint nudge", c.Resize())
	tty.waitUntil(t, "the held output's replay", contains("HELD-OUTPUT"))

	out := tty.String()
	leaveAt := strings.LastIndex(out, "\x1b[?1049l")
	regionAt := strings.LastIndex(out, "\x1b[1;23r")
	cursorAt := strings.LastIndex(out, "\x1b8")
	replayAt := strings.Index(out, "HELD-OUTPUT")
	require.GreaterOrEqual(t, leaveAt, 0, "release leaves the alternate screen")
	assert.Less(t, leaveAt, regionAt, "region re-established on the engine's screen")
	assert.Less(t, regionAt, cursorAt, "engine cursor restored (DECRC) after the bar repaint")
	assert.Less(t, cursorAt, replayAt, "the replay lands on a fully restored screen")

	second := await(t, "the nudge's restore", c.Resize())
	assert.Equal(t, uint16(22), first.Rows, "repaint nudge wiggles a row")
	assert.Equal(t, uint16(23), second.Rows)

	// Interceptor is back to passthrough.
	_, err = ptyDev.Write([]byte("typed-after"))
	require.NoError(t, err)
	engine.waitUntil(t, "passthrough restored", func(s string) bool { return s == "typed-after" })

	select {
	case w := <-warns:
		t.Fatalf("a clean engage/release must not degrade: %s", w)
	default:
	}
}

// TestOverlayComposition_RealOverlayPanicDegradesPermanently extends the
// existing fake-overlay panic tests (controller_test.go's
// TestController_FactoryPanicDegrades /
// TestController_OverlayErrorDegradesToPlainTerminal) to the REAL overlay
// composition: a Sources.Roster call that panics inside the real
// tea.Program's Cmd goroutine is caught by bubbletea's OWN panic recovery
// (tea.go's handleCommands -> recoverFromGoPanic, surfaced as
// ErrProgramPanic through Program.Run()) — proving that recovery composes
// correctly with the Controller's degrade path (interceptor.go:203's
// permanent Off), not just the synthetic error the fake overlay returns.
func TestOverlayComposition_RealOverlayPanicDegradesPermanently(t *testing.T) {
	ptyDev, slave, _ := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	panicSrc := tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) {
			panic("composition test: induced overlay panic")
		},
	}
	resize := make(chan *agent.WindowSize, 4)
	warns := make(chan string, 4)
	c := termui.New(termui.Options{
		Stdin:    slave,
		TTY:      slave,
		Resize:   resize,
		Prefix:   compPrefix,
		Surround: true,
		Bar:      termui.BarInfo{Harp: "h1"},
		NewOverlay: func(start termui.OverlayStart) termui.Overlay {
			return tui.NewOverlay(ctx, panicSrc, compPrefix, start)
		},
		Warn: func(format string, args ...any) { warns <- fmt.Sprintf(format, args...) },
	})
	defer c.Close()
	engine := pumpEngineInput(c)

	resize <- &agent.WindowSize{Rows: 24, Cols: 80}
	await(t, "the initial size", c.Resize())

	_, err := ptyDev.Write([]byte{compPrefix, 'j'})
	require.NoError(t, err)

	w := await(t, "the degradation warning after the real overlay's Roster source panicked", warns)
	assert.Contains(t, w, "plain terminal")
	assert.Contains(t, w, "panic",
		"the real Program's own ErrProgramPanic recovery surfaces through the degrade warning")

	// Permanent: prefix now passes straight through (ixOff, not a one-shot
	// Disengage) — proven against the real Program/pty stack.
	_, err = ptyDev.Write([]byte{compPrefix, 'x'})
	require.NoError(t, err)
	engine.waitUntil(t, "permanent passthrough after the real overlay panicked", func(s string) bool {
		return s == string(compPrefix)+"x"
	})
}
