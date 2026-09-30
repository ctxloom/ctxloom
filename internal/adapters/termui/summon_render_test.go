//go:build !windows

package termui_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// These render the summoned modal's takeover and restore through the real
// controller onto a terminal model, and judge the screen the human sees. The
// modal itself is a stand-in that paints every drawable row: what is drawn
// inside the modal is cli/tui's; where it is drawn and what comes back after
// it is termui's.

// paintedModal paints "MODAL nn" on every drawable row and stays up until
// dismissed.
type paintedModal struct {
	shown chan termui.OverlayGeometry
	quit  chan struct{}
	once  sync.Once
}

func newPaintedModal() *paintedModal {
	return &paintedModal{shown: make(chan termui.OverlayGeometry, 1), quit: make(chan struct{})}
}

func (m *paintedModal) Run(in io.Reader, tty io.Writer, geo termui.OverlayGeometry) error {
	go func() { _, _ = io.Copy(io.Discard, in) }()
	var b strings.Builder
	for r := 1; r <= geo.Rows; r++ {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2KMODAL %02d", r, r)
	}
	if _, err := io.WriteString(tty, b.String()); err != nil {
		return err
	}
	m.shown <- geo
	<-m.quit
	return nil
}

func (m *paintedModal) Abort()                        { m.once.Do(func() { close(m.quit) }) }
func (m *paintedModal) Resize(termui.OverlayGeometry) {}
func (m *paintedModal) Armed(int)                     {}
func (m *paintedModal) Notify(termui.Notice)          {}
func (m *paintedModal) factory() func(*termui.Options) {
	return func(o *termui.Options) {
		o.NewOverlay = func(start termui.OverlayStart) termui.Overlay {
			if !start.Summoned {
				panic("only the summoned modal is scripted here")
			}
			return m
		}
	}
}

func summonModal(t *testing.T, c *termui.Controller, m *paintedModal) {
	t.Helper()
	require.NoError(t, c.Summon(context.Background(), termui.OverlayStart{View: "approvals"}, termui.Notice{Text: "approval from wiry-otter"}))
	select {
	case <-m.shown:
	case <-time.After(5 * time.Second):
		t.Fatal("the modal never painted")
	}
}

func assertModalOwnsTheScreen(t *testing.T, e *vtemu.Screen) {
	t.Helper()
	for r := 1; r < renderRows; r++ {
		assert.Equal(t, fmt.Sprintf("MODAL %02d", r), e.Row(r-1), "the modal covers drawable row %d:\n%s", r, e)
	}
	assert.NotContains(t, e.Row(renderRows-1), "MODAL", "the reserved row is never the modal's")
}

// Over a real engine on the main screen the modal takes the alternate
// screen, and dismissing it hands back exactly the engine's screen — rows,
// bar and cursor — and nudges the engine.
func TestSummonRender_MainScreenEngineGetsItsScreenBack(t *testing.T) {
	m := newPaintedModal()
	h := newPTYEngineHarness(t, "engine", false, m.factory())
	summonModal(t, h.c, m)
	e := h.settle()
	assert.True(t, e.OnAltScreen(), "the modal is on the alternate screen, the engine's screen untouched beneath")
	assertModalOwnsTheScreen(t, e)

	m.Abort()
	waitForComposition(t, "the repaint nudge to reach the engine", func() bool { return h.winches() > 0 })
	assertEngineScreenBack(t, h.settle(), "engine")
}

// Over a real full-screen engine the modal draws on the engine's own
// alternate screen, so nothing of it survives to replay: dismissal clears the
// screen and the engine repaints it on the nudge. The engine's rows are back
// if and only if the nudge crossed its pty.
func TestSummonRender_AltScreenEngineRepaintsOnTheNudge(t *testing.T) {
	m := newPaintedModal()
	h := newPTYEngineHarness(t, "fullscreen", true, m.factory())
	summonModal(t, h.c, m)
	e := h.settle()
	assert.True(t, e.OnAltScreen())
	assertModalOwnsTheScreen(t, e)

	m.Abort()
	waitForComposition(t, "the repaint nudge to reach the engine", func() bool { return h.winches() > 0 })
	e = h.settle()
	assert.True(t, e.OnAltScreen(), "the engine stays on its own screen")
	for i := 1; i < renderRows; i++ {
		assert.Equal(t, fmt.Sprintf("fullscreen row %02d", i), e.Row(i-1), "row %d is the engine's again, and no modal row survives:\n%s", i, e)
	}
	assert.Contains(t, e.Row(renderRows-1), "viewer", "the bar is back on the reserved row")
}

// Engine output held while the modal is up is replayed onto the screen it
// was written for: a row the engine changed behind the modal shows changed.
func TestSummonRender_HeldOutputIsReplayedOntoTheEnginesScreen(t *testing.T) {
	m := newPaintedModal()
	h := newRenderHarness(t, m.factory())
	h.paintEngineRows("engine")
	summonModal(t, h.c, m)
	h.engine("\x1b[5;1H\x1b[2Kchanged behind the modal\x1b[23;1H\x1b[2Kengine row 23")
	assertModalOwnsTheScreen(t, h.settle())

	m.Abort()
	waitForComposition(t, "the repaint nudge", func() bool { return len(h.nudges) > 0 })
	e := h.settle()
	assert.False(t, e.OnAltScreen())
	assert.Equal(t, "changed behind the modal", e.Row(4), "the held write landed on the engine's screen:\n%s", e)
	assert.Equal(t, "engine row 04", e.Row(3))
	assert.Contains(t, e.Row(renderRows-1), "viewer")
}

// A hold that overflowed is not replayed — not even its whole-looking tail:
// the screen is cleared with a notice for the engine to repaint, and no
// fragment of a sequence cut by the overflow ever prints (settle requires
// every byte on the terminal to be understood).
func TestSummonRender_OverflowClearsWithANoticeAndPrintsNoFragment(t *testing.T) {
	m := newPaintedModal()
	h := newRenderHarness(t, m.factory(), func(o *termui.Options) { o.ModalHoldCapacity = 64 })
	h.paintEngineRows("engine")
	summonModal(t, h.c, m)
	h.engine(strings.Repeat("x", 60) + "\x1b[3")
	h.engine("1mTORN-TAIL")

	m.Abort()
	waitForComposition(t, "the repaint nudge", func() bool { return len(h.nudges) > 0 })
	e := h.settle()
	assert.False(t, e.OnAltScreen())
	assert.True(t, strings.HasPrefix(e.Row(0), "ctxloom: engine output overflowed while the overlay"), "the loss is said, on one row:\n%s", e)
	for r := 1; r < renderRows-1; r++ {
		assert.Empty(t, e.Row(r), "row %d is cleared for the engine's repaint:\n%s", r+1, e)
	}
	assert.NotContains(t, e.String(), "TORN-TAIL")
	assert.Contains(t, e.Row(renderRows-1), "viewer", "the bar is back")
}
