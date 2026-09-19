package tui

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the integration harness for tui/overlay.go's Run: a REAL
// tea.Program driven over a pipe (never the tty — overlay.go:38's own
// invariant), asserting behavior (content/escape-sequence presence and
// ordering), never pixel goldens.
// model_test.go already drives Model.Update synthetically; what was
// uncovered before this file is the Program plumbing itself: real Init(),
// real key parsing off the input reader, real renderer writes to the tty
// writer, EnterAltScreen/quit round-tripping through the actual bubbletea
// event loop.

// syncBuffer is a goroutine-safe io.Writer standing in for the tty (the
// Program's renderer and Overlay.Run both write to it from their own
// goroutines).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForOverlay polls until cond holds — a hermetic replacement for sleeps
// around the real Program's own goroutines (repo convention; see
// termui/controller_test.go's waitFor).
//
// The budget is generous on purpose. cond is always an EVENT (a call the
// Program's own command goroutines make, or an escape sequence its renderer
// writes), so waiting longer never turns a failure into a pass — it only
// stops a real event being called absent because a real tea.Program, its
// renderer goroutine and its command goroutines were competing for a CPU
// with every other package `go test ./...` is running at that moment. The
// previous two seconds sat inside that contention's tail, so it expired on
// events that were merely late.
func waitForOverlay(t *testing.T, what string, cond func() bool) {
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

// TestOverlay_RunEngageKeystrokeAltScreenQuitRestores drives the whole
// engagement lifecycle through a real tea.Program: the initial paint reaches
// the tty (engage), a keystroke sent down the input pipe reaches Model.Update
// (round trip), 'f' as the first key enters the alt screen, and 'q' quits —
// unwinding the alt screen on the way out (bubbletea's own contract: "the
// altscreen will be automatically exited when the program quits").
func TestOverlay_RunEngageKeystrokeAltScreenQuitRestores(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "perky-same-chevy", State: "live"})
	o := NewOverlay(context.Background(), f.sources(), 0x1d)

	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuffer
	geo := testGeo()

	done := make(chan error, 1)
	go func() { done <- o.Run(pr, &tty, geo) }()

	// Engage: the real Program's Init() fetches the roster for real and
	// paints it — proving Run wires a live Model, not a stub. (The
	// "loading agents…" transient status is real, from NewModel's initial
	// field, but not asserted here: this fake's roster resolves near-
	// instantly, so waiting for the transient would race the very fetch
	// that clears it — an inherently non-deterministic assertion, dropped
	// rather than forced.)
	waitForOverlay(t, "roster row painted", func() bool {
		return strings.Contains(tty.String(), "perky-same-chevy")
	})
	assert.Contains(t, tty.String(), "j/k move", "the real View() hint line renders through the Program")

	// Keystroke round trip: 'f' is the first key this Model sees, so it is
	// the presentation chord (prefix-then-f = full screen), not a roster
	// navigation key — proving bytes written to the pipe really reach
	// Update via the Program's own input parser.
	_, err := pw.Write([]byte("f"))
	require.NoError(t, err)
	waitForOverlay(t, "alt screen entry sequence", func() bool {
		return strings.Contains(tty.String(), "\x1b[?1049h")
	})

	// Quit: 'q' backs out (m.quit() -> tea.Quit); the Program restores the
	// alt screen on the way out.
	_, err = pw.Write([]byte("q"))
	require.NoError(t, err)
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "a clean quit must not surface as an error")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after q")
	}
	assert.Contains(t, tty.String(), "\x1b[?1049l", "quit restores from the alt screen")
}

// TestOverlay_RunKeystrokeNavigatesRoster proves a plain (non-chord) keystroke
// reaches the model's roster navigation: 'j' selects the second row and opens
// its feed.
func TestOverlay_RunKeystrokeNavigatesRoster(t *testing.T) {
	f := newFakeSources(t.TempDir(),
		RosterRow{Harp: "h1", State: "live"},
		RosterRow{Harp: "h2", State: "executing"},
	)
	o := NewOverlay(context.Background(), f.sources(), 0x1d)

	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuffer
	done := make(chan error, 1)
	go func() { done <- o.Run(pr, &tty, testGeo()) }()

	// Asserted through the WATCH the auto-open causes, for the SAME reason
	// the navigation assertion below is (see its comment): the painted title
	// is only ever a substring of the tty when the roster resolves before the
	// Program's FIRST frame. Lose that race — which is all a busy box has to
	// do — and the roster arrives into a diffed repaint, where bubbletea
	// rewrites only the changed cells and the literal "feed: h1" is never
	// written at all. Waiting longer cannot fix that, which is exactly how
	// this failed: not slowly, but never.
	waitForOverlay(t, "auto-opened first feed", func() bool {
		return slices.Contains(f.watchedHarps(), "h1")
	})

	_, err := pw.Write([]byte("j"))
	require.NoError(t, err)
	// Asserted through the WATCH the navigation causes, not through the
	// painted title. bubbletea v2's renderer diffs frames: switching feeds
	// rewrites the single changed cell (\x1b[9A\x1b[63D2 — move up, move
	// left, print "2"), so the string "feed: h2" is never written to the tty
	// and a substring match cannot see it. Opening h2's feed still proves the
	// keystroke crossed the pipe, reached Model.Update inside a real Program,
	// and that its command ran.
	waitForOverlay(t, "j navigates to the second row's feed", func() bool {
		return slices.Contains(f.watchedHarps(), "h2")
	})

	_, err = pw.Write([]byte("q"))
	require.NoError(t, err)
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after q")
	}
}

// TestOverlay_CtrlCIsAKeystrokeNotASignal pins the "input is never the tty"
// invariant from the other direction: WithoutSignalHandler means bubbletea
// never installs an OS signal handler for this Program, and the input pipe
// is not a term.File, so Program never puts it in raw mode either (tea.go's
// customInput branch is a no-op — see options.go's WithInput). Ctrl+C
// therefore arrives as an ordinary KeyMsg the Model's own updateKey handles
// ("ctrl+c" backs out, same as q), never as a delivered SIGINT.
func TestOverlay_CtrlCIsAKeystrokeNotASignal(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "h1", State: "live"})
	o := NewOverlay(context.Background(), f.sources(), 0x1d)

	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuffer
	done := make(chan error, 1)
	go func() { done <- o.Run(pr, &tty, testGeo()) }()

	waitForOverlay(t, "engaged", func() bool { return strings.Contains(tty.String(), "h1") })

	_, err := pw.Write([]byte{0x03}) // Ctrl+C
	require.NoError(t, err)
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "ctrl+c must quit cleanly through Model.updateKey, not crash or hang")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctrl+c — it may have been swallowed as a signal instead of a keystroke")
	}
}

// TestOverlay_AbortBeforeRunIsSafe pins Abort's before/after-Run safety
// contract (overlay.go's own doc comment on Abort): calling it before Run
// ever starts must not panic, and the subsequent Run must return immediately
// without ever driving the Program (only the panel-position escape Run
// writes ahead of the abort check lands on the tty).
func TestOverlay_AbortBeforeRunIsSafe(t *testing.T) {
	f := newFakeSources(t.TempDir())
	o := NewOverlay(context.Background(), f.sources(), 0x1d)
	o.Abort() // before Run — must be a safe no-op-marking call

	pr, pw := io.Pipe()
	defer pw.Close()
	defer pr.Close()
	var tty syncBuffer

	done := make(chan error, 1)
	go func() { done <- o.Run(pr, &tty, testGeo()) }()

	select {
	case err := <-done:
		require.NoError(t, err, "an aborted-before-start overlay must return cleanly")
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return immediately when already aborted, not drive the Program")
	}
	assert.Equal(t, "\x1b[21;1H", tty.String(),
		"only the pre-abort-check panel-position write lands; the Program itself never runs")
}

// blockingQuitter stands in for a tea.Program whose event loop is not draining
// its (unbuffered) message channel: Quit blocks, exactly as the real one does
// between Run publishing the program and Run reaching the loop, and after the
// loop has exited.
type blockingQuitter struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingQuitter) Quit() {
	close(b.entered)
	<-b.release
}

// Abort must not hold the overlay's lock while Quit blocks. Run takes that same
// lock immediately after p.Run returns, so an Abort parked inside Quit with the
// lock held wedges the pair permanently — and Run is what restores the engine's
// terminal.
func TestOverlay_AbortDoesNotHoldTheLockWhileQuitBlocks(t *testing.T) {
	f := newFakeSources(t.TempDir())
	o := NewOverlay(context.Background(), f.sources(), 0x1d)
	bq := &blockingQuitter{entered: make(chan struct{}), release: make(chan struct{})}
	o.prog = bq
	defer close(bq.release)

	go o.Abort()
	select {
	case <-bq.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort never reached Quit")
	}

	free := make(chan struct{})
	go func() {
		o.mu.Lock()
		o.mu.Unlock() //nolint:staticcheck // the probe is the acquisition itself
		close(free)
	}()
	select {
	case <-free:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort holds the overlay lock while blocked inside Quit; Run cannot finish and the terminal is never restored")
	}
}
