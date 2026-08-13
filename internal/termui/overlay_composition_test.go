//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/cli/tui"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/termui"
)

// This file is the F1 termui<->tui COMPOSITION harness (Wave F playbook,
// item 2): controller_test.go (package termui, internal) already proves the
// Controller's engage/hold/replay/nudge/degrade lifecycle against a
// fakeOverlay; what was uncovered is the REAL seam production wires at
// run_terminal_ui.go:77 (NewOverlay: func() termui.Overlay { return
// tui.NewOverlay(ctx, src, prefix) }) — the actual bubbletea Program running
// behind the Controller, over a REAL pty (aymanbagabas/go-pty; no tmux/
// teatest/vt10x per the playbook's binding principles). Because tui imports
// termui (roster.go), this file is package termui_test (an external test
// package) — an internal termui test importing tui would cycle.
//
// syncBuf/waitForComposition/recvWindowSize deliberately re-derive
// controller_test.go's lockedBuffer/waitFor/drainTranslated idioms rather
// than reuse them: those helpers are unexported to package termui's internal
// tests and unreachable from this external package.

const compPrefix byte = 0x1d

// syncBuf is a goroutine-safe io.Writer standing in for the tty side a real
// pty's master-read loop feeds.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitForComposition polls until cond holds — hermetic, no bare sleeps as
// synchronization (Wave F playbook timing constraint).
//
// The budget is generous on purpose. cond is always an EVENT (bytes the real
// bubbletea renderer or the Controller wrote through a real pty, or a call
// the overlay's own command goroutines made), so a longer wait never turns a
// failure into a pass — it only stops a real event being called absent
// because a pty round trip, a tea.Program and its renderer were competing for
// a CPU with every other package `go test ./...` is running at that moment.
// The previous three seconds sat inside that contention's tail, so it expired
// on events that were merely late.
func waitForComposition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func recvWindowSize(t *testing.T, ch <-chan *pb.WindowSize) *pb.WindowSize {
	t.Helper()
	select {
	case ws := <-ch:
		return ws
	case <-time.After(3 * time.Second):
		t.Fatal("no resize event arrived")
		return nil
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
// It also RECORDS the harps the overlay asked to watch: that call is the only
// frame-diff-proof evidence that a feed was opened (see the engage assertion).
type recordedSources struct {
	mu      sync.Mutex
	watched []string
}

func (r *recordedSources) watchedHarps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.watched...)
}

func realOverlaySources(harp string) (tui.Sources, *recordedSources) {
	rec := &recordedSources{}
	return tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) {
			return []tui.RosterRow{{Harp: harp, State: "live"}}, nil
		},
		Watch: func(_ context.Context, h string) (*tui.Feed, error) {
			rec.mu.Lock()
			rec.watched = append(rec.watched, h)
			rec.mu.Unlock()
			return &tui.Feed{
				Source: "live",
				Events: make(chan operations.SessionFeedEvent),
				Errs:   make(chan error, 1),
				Cancel: func() {},
			}, nil
		},
		Now: time.Now,
	}, rec
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
// output held during engagement; the atomic release ordering: panel clear →
// scroll region restore → engine cursor restore (DECRC) → held-output
// replay; a post-release resize nudge) against the GENUINE bubbletea Program
// and a GENUINE pty, not the fake.
func TestOverlayComposition_EngageHoldReplayNudge(t *testing.T) {
	ptyDev, slave, tty := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, watched := realOverlaySources("perky-same-chevy")

	resize := make(chan *pb.WindowSize, 4)
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
		NewOverlay: func() termui.Overlay { return tui.NewOverlay(ctx, src, compPrefix) },
		Warn:       func(format string, args ...any) { warns <- fmt.Sprintf(format, args...) },
	})
	defer c.Close()
	engine := pumpEngineInput(c)

	resize <- &pb.WindowSize{Rows: 24, Cols: 80}
	ws := recvWindowSize(t, c.Resize())
	require.Equal(t, uint32(23), ws.Rows, "initial size reaches the engine reserved")
	waitForComposition(t, "surround establish", func() bool { return strings.Contains(tty.String(), "\x1b[1;23r") })

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
	waitForComposition(t, "the real overlay painted its panel on the pty", func() bool {
		return strings.Contains(tty.String(), "feed:")
	})
	waitForComposition(t, "the real overlay auto-opened the roster row's feed", func() bool {
		return slices.Contains(watched.watchedHarps(), "perky-same-chevy")
	})
	assert.Contains(t, tty.String(), "\x1b7\x1b[r",
		"engage saves the engine cursor and hands the overlay the full scroll region")

	// Engine output during engagement is held (never reaches the pty).
	before := tty.String()
	_, _ = c.Stdout().Write([]byte("HELD-OUTPUT"))
	assert.NotContains(t, tty.String(), "HELD-OUTPUT")
	assert.Equal(t, before, tty.String(), "nothing hits the pty while held")

	// Release: 'q' backs the REAL overlay Model out (tea.Quit); the
	// Controller's one atomic restore write + replay + nudge follow.
	_, err = ptyDev.Write([]byte("q"))
	require.NoError(t, err)
	waitForComposition(t, "held output replayed", func() bool { return strings.Contains(tty.String(), "HELD-OUTPUT") })

	out := tty.String()
	clearAt := strings.LastIndex(out, "\x1b[16;1H\x1b[J") // drawable-panelRows+1 = (24-1)-8+1
	regionAt := strings.LastIndex(out, "\x1b[1;23r")
	cursorAt := strings.LastIndex(out, "\x1b8")
	replayAt := strings.Index(out, "HELD-OUTPUT")
	require.GreaterOrEqual(t, clearAt, 0, "panel region cleared")
	assert.Less(t, clearAt, regionAt, "region re-established after the clear")
	assert.Less(t, regionAt, cursorAt, "engine cursor restored (DECRC) after the bar repaint")
	assert.Less(t, cursorAt, replayAt, "the replay lands on a fully restored screen")

	first := recvWindowSize(t, c.Resize())
	second := recvWindowSize(t, c.Resize())
	assert.Equal(t, uint32(22), first.Rows, "repaint nudge wiggles a row")
	assert.Equal(t, uint32(23), second.Rows)

	// Interceptor is back to passthrough.
	_, err = ptyDev.Write([]byte("typed-after"))
	require.NoError(t, err)
	waitForComposition(t, "passthrough restored", func() bool { return engine.String() == "typed-after" })

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
	resize := make(chan *pb.WindowSize, 4)
	warns := make(chan string, 4)
	c := termui.New(termui.Options{
		Stdin:      slave,
		TTY:        slave,
		Resize:     resize,
		Prefix:     compPrefix,
		Surround:   true,
		Bar:        termui.BarInfo{Harp: "h1"},
		NewOverlay: func() termui.Overlay { return tui.NewOverlay(ctx, panicSrc, compPrefix) },
		Warn:       func(format string, args ...any) { warns <- fmt.Sprintf(format, args...) },
	})
	defer c.Close()
	engine := pumpEngineInput(c)

	resize <- &pb.WindowSize{Rows: 24, Cols: 80}
	_ = recvWindowSize(t, c.Resize())

	_, err := ptyDev.Write([]byte{compPrefix, 'j'})
	require.NoError(t, err)

	select {
	case w := <-warns:
		assert.Contains(t, w, "plain terminal")
		assert.Contains(t, w, "panic",
			"the real Program's own ErrProgramPanic recovery surfaces through the degrade warning")
	case <-time.After(3 * time.Second):
		t.Fatal("no degradation warning after the real overlay's Roster source panicked")
	}

	// Permanent: prefix now passes straight through (ixOff, not a one-shot
	// Disengage) — proven against the real Program/pty stack.
	_, err = ptyDev.Write([]byte{compPrefix, 'x'})
	require.NoError(t, err)
	waitForComposition(t, "permanent passthrough after the real overlay panicked", func() bool {
		return engine.String() == string(compPrefix)+"x"
	})
}
