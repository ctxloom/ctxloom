//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	pty "github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// This file renders the REAL controller and the REAL bubbletea overlay
// through a terminal model (testsupport/vtemu) and asserts what the human
// would SEE. overlay_composition_test.go asserts which bytes were written; a
// panel whose rows each start where the previous one ended — every row after
// the first, on a raw tty that adds no carriage return — passes every one of
// those assertions and is unreadable.

const renderRows, renderCols = 24, 80

// renderHarps are the roster the overlay shows, in order.
var renderHarps = []string{"harp-alpha", "harp-bravo", "harp-charlie"}

type renderHarness struct {
	t       *testing.T
	pty     pty.Pty
	tty     *syncBuf
	c       *termui.Controller
	nudges  <-chan *agent.WindowSize
	watches <-chan string // the harps the overlay opened, as it opens them
	slave   *os.File      // the controller's side of the terminal
	fences  int
}

// renderSources is the overlay's roster of renderHarps, reporting every feed
// it opens on the returned channel.
func renderSources() (tui.Sources, <-chan string) {
	watches := make(chan string, 16)
	rows := make([]tui.RosterRow, len(renderHarps))
	for i, h := range renderHarps {
		rows[i] = tui.RosterRow{Harp: h, State: "live"}
	}
	return tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) { return rows, nil },
		Watch: func(_ context.Context, h string) (*tui.Feed, error) {
			watches <- h
			return &tui.Feed{Source: "live", Events: make(chan operations.SessionFeedEvent), Errs: make(chan error, 1), Cancel: func() {}}, nil
		},
	}, watches
}

func newRenderHarness(t *testing.T, opts ...func(*termui.Options)) *renderHarness {
	t.Helper()
	ptyDev, slave, tty := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	src, watches := renderSources()
	resize := make(chan *agent.WindowSize, 4)
	o := termui.Options{
		Stdin: slave, TTY: slave, Resize: resize, Prefix: compPrefix, Surround: true,
		Bar:        termui.BarInfo{Harp: "self-session", Engine: "mock", PrefixHint: "^]"},
		NewOverlay: func(start termui.OverlayStart) termui.Overlay { return tui.NewOverlay(ctx, src, compPrefix, start) },
	}
	for _, f := range opts {
		f(&o)
	}
	c := termui.New(o)
	t.Cleanup(c.Close)
	pumpEngineInput(c)
	resize <- &agent.WindowSize{Rows: renderRows, Cols: renderCols}
	await(t, "the initial size", c.Resize())
	tty.waitUntil(t, "the surround's region", contains("\x1b[1;23r"))
	return &renderHarness{t: t, pty: ptyDev, tty: tty, c: c, nudges: c.Resize(), watches: watches, slave: slave}
}

// engine writes through the controller's engine-output seam, as the pty pump
// does.
func (h *renderHarness) engine(s string) {
	h.t.Helper()
	_, err := h.c.Stdout().Write([]byte(s))
	require.NoError(h.t, err)
}

// paintEngineRows fills every drawable row with a labelled line, leaving the
// cursor where the last write ended.
func (h *renderHarness) paintEngineRows(label string) {
	h.t.Helper()
	for i := 1; i < renderRows; i++ {
		h.engine(fmt.Sprintf("\x1b[%d;1H%s row %02d", i, label, i))
	}
	h.tty.waitUntil(h.t, "the engine's screen", contains(fmt.Sprintf("%s row %02d", label, renderRows-1)))
}

func (h *renderHarness) key(s string) {
	h.t.Helper()
	_, err := h.pty.Write([]byte(s))
	require.NoError(h.t, err)
}

// released waits out a release: its nudge is sent once the release's write to
// the terminal has returned, and a fence behind that write orders all of it
// before anything is judged — a cleared row or an absent fragment is only
// evidence once nothing of the release is still in the pty.
func (h *renderHarness) released() {
	h.t.Helper()
	await(h.t, "the repaint nudge", h.nudges)
	h.fences++
	// A DECRQM query: inert to the screen, and unique, so its arrival is the
	// fence's.
	f := fmt.Sprintf("\x1b[?%d$p", 7700+h.fences)
	_, err := io.WriteString(h.slave, f)
	require.NoError(h.t, err)
	h.tty.waitUntil(h.t, "the fence behind the release", contains(f))
}

// tb is what a screen check reports to: the test, or a probe judging a frame
// that may not have finished arriving.
type tb interface {
	require.TestingT
	Helper()
}

// probe records a check's failure instead of failing the test.
type probe struct{ failed bool }

func (p *probe) Errorf(string, ...any) { p.failed = true }
func (p *probe) FailNow()              { p.failed = true; panic(p) }
func (p *probe) Helper()               {}

// accepts reports whether check passes on e.
func accepts(check func(tb, *vtemu.Screen), e *vtemu.Screen) (ok bool) {
	p := &probe{}
	defer func() {
		if r := recover(); r != nil && r != any(p) {
			panic(r)
		}
		ok = !p.failed
	}()
	check(p, e)
	return true
}

// emulate is the screen the bytes so far paint.
func emulate(s string) *vtemu.Screen {
	e := vtemu.New(renderRows, renderCols)
	e.Feed([]byte(s))
	return e
}

// screenWhen waits until the terminal shows a frame check accepts, judged
// again on every write that arrives, and returns it. A frame is the
// tea.Program's to finish in its own time; this waits for the frame the test
// expects rather than for a quiet spell that a loaded machine can fake. At
// the deadline check runs against the test, so a frame that never came fails
// with what the screen actually shows.
func (h *renderHarness) screenWhen(what string, check func(tb, *vtemu.Screen)) *vtemu.Screen {
	h.t.Helper()
	judged := func(t tb, e *vtemu.Screen) {
		t.Helper()
		require.Empty(t, e.Unhandled(), "every byte on the terminal must be understood before a frame is judged")
		check(t, e)
	}
	cur, ok := h.tty.await(h.t, func(s string) bool { return accepts(judged, emulate(s)) })
	e := emulate(cur)
	judged(h.t, e)
	if !ok {
		h.t.Fatalf("the terminal never showed %s", what)
	}
	return e
}

// panel is a screenWhen check for assertPanel.
func panel(top, bottom int) func(tb, *vtemu.Screen) {
	return func(t tb, e *vtemu.Screen) { t.Helper(); assertPanel(t, e, top, bottom) }
}

// engineBack is a screenWhen check for assertEngineScreenBack.
func engineBack(label string) func(tb, *vtemu.Screen) {
	return func(t tb, e *vtemu.Screen) { t.Helper(); assertEngineScreenBack(t, e, label) }
}

// assertPanel checks the overlay's frame occupies rows [top, bottom] as a
// panel should: the header on the first row, one roster row per harp with
// the pane separator in the header's column, the key hints on the last row,
// and nothing pushed into the scrollback.
func assertPanel(t tb, e *vtemu.Screen, top, bottom int) {
	t.Helper()
	header := e.Row(top)
	require.True(t, strings.HasPrefix(header, " agents"), "the header starts the panel's first row at column 0:\n%s", e)
	sep := strings.IndexRune(header, '│')
	require.Positive(t, sep, "the header carries the pane separator:\n%s", e)
	sepCol := len([]rune(header[:sep]))
	for r := top + 1; r < bottom; r++ {
		assert.Equal(t, '│', e.Cell(r, sepCol), "row %d keeps the pane separator in the header's column:\n%s", r+1, e)
	}
	for i, h := range renderHarps {
		row := []rune(e.Row(top + 1 + i))
		left := strings.TrimSpace(string(row[:min(sepCol, len(row))]))
		assert.True(t, strings.HasSuffix(left, h), "roster row %d reads %q, want the harp %q alone:\n%s", i, left, h, e)
		assert.Equal(t, 1, strings.Count(e.Row(top+1+i), h), "the harp is painted once on its row:\n%s", e)
	}
	assert.True(t, strings.HasPrefix(e.Row(bottom), " j/k move"), "the key hints are the panel's last row:\n%s", e)
	assert.Empty(t, e.Scrollback(), "the panel must not scroll the screen")
}

// assertEngineScreenBack checks release handed back exactly the screen the
// engine had: every drawable row, the bar, and the engine's cursor.
func assertEngineScreenBack(t tb, e *vtemu.Screen, label string) {
	t.Helper()
	assert.False(t, e.OnAltScreen(), "release returns to the engine's screen")
	for i := 1; i < renderRows; i++ {
		assert.Equal(t, fmt.Sprintf("%s row %02d", label, i), e.Row(i-1), "engine row %d is restored:\n%s", i, e)
	}
	assert.Contains(t, e.Row(renderRows-1), "viewer", "the bar is back on the reserved row")
	r, c := e.Cursor()
	assert.Equal(t, [2]int{renderRows - 2, len(label) + len(" row 23")}, [2]int{r, c}, "the engine's cursor is where the engine left it")
}

// The quick panel paints as a panel, survives navigation, and closing it
// restores the engine's screen exactly.
func TestOverlayRender_QuickPanelIsReadableAndReleaseRestoresTheEngine(t *testing.T) {
	h := newRenderHarness(t)
	h.paintEngineRows("engine")

	h.key(string([]byte{compPrefix}))
	awaitWatch(t, h.watches, renderHarps[0])
	// Drawable rows are 23 (the bar reserves one); the panel is the bottom 8
	// of them: rows 16..23, 0-indexed 15..22.
	h.screenWhen("the panel", panel(15, 22))

	h.key("j") // moving the selection opens that row's feed
	awaitWatch(t, h.watches, renderHarps[1])
	h.screenWhen("the panel after moving", panel(15, 22))

	h.key("\r")
	h.screenWhen("the panel on the opened feed", func(t tb, e *vtemu.Screen) {
		assertPanel(t, e, 15, 22)
		assert.Contains(t, e.Row(15), "feed: "+renderHarps[1], "the header names the opened feed")
	})

	h.key("q")
	h.released()
	h.screenWhen("the engine's screen back", engineBack("engine"))
}

// Full screen draws the whole drawable area, and leaving it — straight from
// full screen, the case that used to erase the engine's bottom rows —
// restores the engine's screen exactly.
func TestOverlayRender_FullScreenIsReadableAndReleaseRestoresTheEngine(t *testing.T) {
	h := newRenderHarness(t)
	h.paintEngineRows("engine")

	h.key(string([]byte{compPrefix}) + "f")
	awaitWatch(t, h.watches, renderHarps[0])
	h.screenWhen("full screen", func(t tb, e *vtemu.Screen) {
		assert.True(t, e.OnAltScreen(), "full screen draws on the alternate screen")
		assertPanel(t, e, 0, renderRows-2)
	})

	h.key("q")
	h.released()
	h.screenWhen("the engine's screen back", engineBack("engine"))
}

// An engine that is itself on the alternate screen is drawn over in place:
// the panel is as readable, full screen is refused with a note the footer
// actually shows at 80 columns, and release leaves the engine on its own
// screen with everything above the panel untouched. What the panel covered
// is the engine's to repaint on the nudge, which a full-screen program does.
func TestOverlayRender_OverAnAltScreenEngineTheOverlayDrawsInPlace(t *testing.T) {
	h := newRenderHarness(t)
	h.engine("\x1b[?1049h")
	h.paintEngineRows("fullscreen")

	h.key(string([]byte{compPrefix}))
	awaitWatch(t, h.watches, renderHarps[0])
	h.screenWhen("the panel", panel(15, 22))
	// Still the first key. Sent once the panel is up, as a human types it:
	// the first roster's feed auto-open clears the note slot.
	h.key("f")
	h.screenWhen("the refusal", func(t tb, e *vtemu.Screen) {
		assert.True(t, e.OnAltScreen(), "still the engine's screen")
		for i := 1; i <= 15; i++ {
			assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "the engine's rows above the panel are untouched:\n%s", e)
		}
		assert.True(t, strings.HasPrefix(e.Row(15), " agents"), "the panel, not full screen:\n%s", e)
		assert.True(t, strings.HasPrefix(e.Row(22), " full screen unavailable"), "the refusal is readable at 80 columns:\n%s", e)
	})

	h.key("q")
	h.released()
	h.screenWhen("the cleared panel region", func(t tb, e *vtemu.Screen) {
		assert.True(t, e.OnAltScreen(), "release must not take the engine off its own screen")
		for i := 1; i <= 15; i++ {
			assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "row %d:\n%s", i, e)
		}
		for i := 16; i <= 23; i++ {
			assert.Empty(t, e.Row(i-1), "the panel region is cleared for the engine's repaint:\n%s", e)
		}
	})
}
