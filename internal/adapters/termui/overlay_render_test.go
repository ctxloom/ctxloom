//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	watched func() []string
}

func newRenderHarness(t *testing.T) *renderHarness {
	t.Helper()
	ptyDev, slave, tty := newComposedPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	var watched []string
	rows := make([]tui.RosterRow, len(renderHarps))
	for i, h := range renderHarps {
		rows[i] = tui.RosterRow{Harp: h, State: "live"}
	}
	src := tui.Sources{
		Roster: func(context.Context) ([]tui.RosterRow, error) { return rows, nil },
		Watch: func(_ context.Context, h string) (*tui.Feed, error) {
			mu.Lock()
			watched = append(watched, h)
			mu.Unlock()
			return &tui.Feed{Source: "live", Events: make(chan operations.SessionFeedEvent), Errs: make(chan error, 1), Cancel: func() {}}, nil
		},
	}
	resize := make(chan *agent.WindowSize, 4)
	c := termui.New(termui.Options{
		Stdin: slave, TTY: slave, Resize: resize, Prefix: compPrefix, Surround: true,
		Bar:        termui.BarInfo{Harp: "self-session", Engine: "mock", PrefixHint: "^]"},
		NewOverlay: func(termui.OverlayStart) termui.Overlay { return tui.NewOverlay(ctx, src, compPrefix) },
	})
	t.Cleanup(c.Close)
	pumpEngineInput(c)
	resize <- &agent.WindowSize{Rows: renderRows, Cols: renderCols}
	recvWindowSize(t, c.Resize())
	waitForComposition(t, "surround establish", func() bool { return strings.Contains(tty.String(), "\x1b[1;23r") })
	return &renderHarness{t: t, pty: ptyDev, tty: tty, c: c, nudges: c.Resize(), watched: func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(watched)
	}}
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
	want := fmt.Sprintf("%s row %02d", label, renderRows-1)
	waitForComposition(h.t, "the engine's screen", func() bool { return strings.Contains(h.tty.String(), want) })
}

func (h *renderHarness) key(s string) {
	h.t.Helper()
	_, err := h.pty.Write([]byte(s))
	require.NoError(h.t, err)
}

// settle waits until the terminal has been quiet for a while — a frame is
// only judged once nothing more is coming — and returns what it shows.
func (h *renderHarness) settle() *vtemu.Screen {
	h.t.Helper()
	last, quietSince := -1, time.Now()
	waitForComposition(h.t, "the terminal to go quiet", func() bool {
		if n := len(h.tty.String()); n != last {
			last, quietSince = n, time.Now()
			return false
		}
		return time.Since(quietSince) > 300*time.Millisecond
	})
	e := vtemu.New(renderRows, renderCols)
	e.Feed([]byte(h.tty.String()))
	require.Empty(h.t, e.Unhandled(), "every byte on the terminal must be understood before a frame is judged")
	return e
}

// assertPanel checks the overlay's frame occupies rows [top, bottom] as a
// panel should: the header on the first row, one roster row per harp with
// the pane separator in the header's column, the key hints on the last row,
// and nothing pushed into the scrollback.
func assertPanel(t *testing.T, e *vtemu.Screen, top, bottom int) {
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
func assertEngineScreenBack(t *testing.T, e *vtemu.Screen, label string) {
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
	waitForComposition(t, "the first roster row's feed opened", func() bool { return slices.Contains(h.watched(), renderHarps[0]) })
	// Drawable rows are 23 (the bar reserves one); the panel is the bottom 8
	// of them: rows 16..23, 0-indexed 15..22.
	assertPanel(t, h.settle(), 15, 22)

	h.key("j")
	assertPanel(t, h.settle(), 15, 22)

	h.key("\r")
	waitForComposition(t, "the selected row's feed opened", func() bool { return slices.Contains(h.watched(), renderHarps[1]) })
	e := h.settle()
	assertPanel(t, e, 15, 22)
	assert.Contains(t, e.Row(15), "feed: "+renderHarps[1], "the header names the opened feed")

	h.key("q")
	waitForComposition(t, "the repaint nudge", func() bool { return len(h.nudges) > 0 })
	assertEngineScreenBack(t, h.settle(), "engine")
}

// Full screen draws the whole drawable area, and leaving it — straight from
// full screen, the case that used to erase the engine's bottom rows —
// restores the engine's screen exactly.
func TestOverlayRender_FullScreenIsReadableAndReleaseRestoresTheEngine(t *testing.T) {
	h := newRenderHarness(t)
	h.paintEngineRows("engine")

	h.key(string([]byte{compPrefix}) + "f")
	waitForComposition(t, "the first roster row's feed opened", func() bool { return slices.Contains(h.watched(), renderHarps[0]) })
	e := h.settle()
	assert.True(t, e.OnAltScreen(), "full screen draws on the alternate screen")
	assertPanel(t, e, 0, renderRows-2)

	h.key("q")
	waitForComposition(t, "the repaint nudge", func() bool { return len(h.nudges) > 0 })
	assertEngineScreenBack(t, h.settle(), "engine")
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
	waitForComposition(t, "the first roster row's feed opened", func() bool { return slices.Contains(h.watched(), renderHarps[0]) })
	h.settle()
	// Still the first key. Sent once the panel is up, as a human types it:
	// the first roster's feed auto-open clears the note slot.
	h.key("f")
	e := h.settle()
	assert.True(t, e.OnAltScreen(), "still the engine's screen")
	for i := 1; i <= 15; i++ {
		assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "the engine's rows above the panel are untouched:\n%s", e)
	}
	assert.True(t, strings.HasPrefix(e.Row(15), " agents"), "the panel, not full screen:\n%s", e)
	assert.True(t, strings.HasPrefix(e.Row(22), " full screen unavailable"), "the refusal is readable at 80 columns:\n%s", e)

	h.key("q")
	waitForComposition(t, "the repaint nudge", func() bool { return len(h.nudges) > 0 })
	e = h.settle()
	assert.True(t, e.OnAltScreen(), "release must not take the engine off its own screen")
	for i := 1; i <= 15; i++ {
		assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "row %d:\n%s", i, e)
	}
	for i := 16; i <= 23; i++ {
		assert.Empty(t, e.Row(i-1), "the panel region is cleared for the engine's repaint:\n%s", e)
	}
}
