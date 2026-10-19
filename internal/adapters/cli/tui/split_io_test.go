package tui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termemu"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

func treeHas(h *splitHarness, text string) bool {
	for _, r := range h.screen() {
		if strings.Contains(strings.SplitN(r, "│", 2)[0], text) {
			return true
		}
	}
	return false
}

// The roster's first state is the watch's snapshot; what no event carries
// (the context gauge here) shows only once an event has triggered a fresh
// read — one read per burst, after the debounce — or the safety tick has.
func TestSplit_RosterFollowsTheRunWatchDebounced(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	h := newSplitHarness(t, port).sized(100, 12)
	require.True(t, treeHas(h, "brave-heron·coder"), "the snapshot is the first roster")
	require.Zero(t, port.runReads(), "the snapshot needed no read of its own")

	port.setRuns(func(r []coord.RunInfo) { r[0].ContextTokens, r[0].ContextWindow = 50_000, 200_000 })
	h.send(rosterEventsMsg{})
	h.send(rosterEventsMsg{})
	h.advance(rosterDebounce - time.Millisecond)
	assert.Zero(t, port.runReads(), "inside the debounce nothing is read")
	h.send(rosterEventsMsg{})
	h.advance(time.Millisecond)
	assert.Equal(t, 1, port.runReads(), "a burst of events is one read")
	assert.True(t, treeHas(h, "25%"), "the fresh read carries the gauge")

	port.setRuns(func(r []coord.RunInfo) { r[0].ContextTokens = 100_000 })
	h.advance(rosterSafetyEvery - rosterDebounce)
	assert.Equal(t, 2, port.runReads(), "the safety tick reads with no event")
	assert.True(t, treeHas(h, "50%"))
	h.advance(rosterSafetyEvery)
	assert.Equal(t, 3, port.runReads(), "and keeps ticking")

	h.send(rosterEventsMsg{closed: true})
	assert.Nil(t, h.s.runEvents, "a closed watch is let go")
}

// The pump hands a burst — loss markers included — over as one message, and
// says when the watch has closed.
func TestSplit_RunEventPumpCoalescesABurst(t *testing.T) {
	port := newSplitPort()
	h := newSplitHarness(t, port)
	h.s.driven = false
	port.events <- coord.Event{RunID: "r1"}
	port.events <- coord.Event{Payload: coord.EventsLost{}}
	port.events <- coord.Event{RunID: "r2"}
	assert.Equal(t, rosterEventsMsg{}, h.s.waitRunEvents()())
	assert.Empty(t, port.events, "the burst was drained")
	port.events <- coord.Event{RunID: "r3"}
	close(port.events)
	assert.Equal(t, rosterEventsMsg{closed: true}, h.s.waitRunEvents()(), "a close behind queued events")
	h.s.runEvents = nil
	assert.Nil(t, h.s.waitRunEvents(), "no watch, no pump")
}

func TestSplit_WithNoCoordinatorTheIndexAloneOnTheSlowTick(t *testing.T) {
	port := newSplitPort()
	port.watchErr = frontend.ErrNoCoordinator
	port.sessions = append(port.sessions, frontend.Session{Harp: "old-moth", Engine: "codex", State: frontend.SessionEnded})
	h := newSplitHarness(t, port).sized(100, 12)
	assert.True(t, treeHas(h, "old-moth"))
	assert.Equal(t, 1, port.runReads())
	h.advance(rosterSafetyEvery)
	assert.Equal(t, 2, port.runReads())
}

func TestSplit_RosterErrorIsTheNoteUntilTheNextRead(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.send(rosterErrMsg{err: assert.AnError})
	assert.True(t, h.panelHas("roster: assert.AnError"))
	h.send(rosterMsg{rows: []RosterRow{{Harp: selfHarp}}})
	assert.False(t, h.panelHas("roster:"))
}

// A child that leaves the roster hands the selection back to the engine.
func TestSplit_SelectionOnAVanishedChildFallsBackToTheEngine(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	h := newSplitHarness(t, port).sized(100, 12)
	h.typeBytes("\x1d")
	h.keys("j")
	require.Equal(t, "brave-heron", h.s.sel)
	h.send(rosterMsg{rows: []RosterRow{{Harp: selfHarp}}})
	assert.Equal(t, selfHarp, h.s.sel)
	assert.Equal(t, "brave-heron", port.released(t), "its feed was released")
	assert.True(t, h.s.showingTerminal())
}

// The engine is sized only once the human's colours are known — it asks for
// them as it starts — or once colorWait has passed without them.
func TestSplit_FirstResizeWaitsForTheHumansColours(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	assert.Empty(t, h.engine.resizes(), "sized before the colours are known")
	assert.NotEmpty(t, h.screen(), "the panel draws meanwhile")
	bg := color.RGBA{R: 0xfe, G: 0xfd, B: 0xfc, A: 0xff}
	h.send(tea.BackgroundColorMsg{Color: bg})
	assert.Equal(t, [][2]uint16{{30, 73}}, h.engine.resizes())
	cols, rows := h.pane.Size()
	assert.Equal(t, [2]int{73, 30}, [2]int{cols, rows})

	_, _ = h.pane.Write([]byte("\x1b]11;?\x07"))
	h.engine.waitFor(t, "\a")
	assert.Equal(t, ansi.SetBackgroundColor(ansi.XRGBColor{Color: bg}.String()), h.engine.take(), "the engine is told the human's background")

	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	assert.Len(t, h.engine.resizes(), 1, "the same size is not resent")
	h.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	assert.Equal(t, [2]uint16{30, 93}, h.engine.resizes()[1])
}

func TestSplit_FirstResizeStopsWaitingAfterColorWait(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.advance(colorWait - time.Millisecond)
	assert.Empty(t, h.engine.resizes())
	h.advance(time.Millisecond)
	assert.Equal(t, [][2]uint16{{30, 73}}, h.engine.resizes())
}

func TestSplit_MouseOverThePaneReachesTheEngineOffset(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	h := newSplitHarness(t, port).sized(100, 12)
	h.engineWrites("\x1b[?1000h\x1b[?1006h")
	h.settleEngine(t)
	// The human's column 40 (1-based) is pane column 40-27=13.
	h.typeBytes("\x1b[<0;40;5M\x1b[<0;40;5m")
	h.typeBytes("\x1b[<0;3;5M\x1b[<0;27;5M") // the panel and the divider
	h.typeBytes("\x1b[<64;28;2M")            // the pane's first column
	assert.Equal(t, "\x1b[<0;13;5M\x1b[<0;13;5m\x1b[<64;1;2M", h.settleEngine(t))

	h.typeBytes("\x1d")
	h.keys("j") // a row that is not the engine's: nothing reaches it
	h.typeBytes("\x1b[<0;40;5M")
	assert.Empty(t, h.settleEngine(t))
}

// A mouse event crosses the pane's reply pipe: SendMouse returns once the
// drain has taken it, before it reaches the engine. Held there, a direct read
// misses it; settleEngine, whose query queues behind it, never does.
func TestSplit_MouseInFlightIsSettledBeforeTheEngineIsRead(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.engineWrites("\x1b[?1000h\x1b[?1006h")
	h.settleEngine(t)
	h.engine.hold(t)
	h.typeBytes("\x1b[<64;28;2M")
	assert.Empty(t, h.engine.String(), "in flight: a direct read misses it")
	h.engine.release()
	assert.Equal(t, "\x1b[<64;1;2M", h.settleEngine(t))
}

// What the engine asks of the terminal beyond its screen reaches the human's.
func TestSplit_PaneEventsReachTheHumansTerminal(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 12)
	mb := newPaneMailbox()
	cb := mb.callbacks()
	cb.Title("✳ Claude")
	cb.Bell()
	cb.Clipboard('c', "copied")
	cb.Clipboard('p', "primary")
	cb.Mouse(termemu.MouseDrags)
	h.send(paneEventsMsg(mb.take()))
	v := h.s.View()
	assert.Equal(t, "✳ Claude", v.WindowTitle)
	assert.Equal(t, tea.MouseModeCellMotion, v.MouseMode)
	assert.Equal(t, []string{"\a"}, h.raws())
	assert.Contains(t, h.out, tea.SetClipboard("copied")())
	assert.Contains(t, h.out, tea.SetPrimaryClipboard("primary")())

	h.send(paneEventsMsg{paneMouseMsg(termemu.MouseMotion)})
	assert.Equal(t, tea.MouseModeAllMotion, h.s.View().MouseMode)
	h.send(paneEventsMsg{paneMouseMsg(termemu.MouseOff)})
	assert.Equal(t, tea.MouseModeNone, h.s.View().MouseMode, "the engine asked for none: native selection")
	h.send(paneEventsMsg{paneMouseMsg(termemu.MouseClicks)})
	h.typeBytes("\x1d")
	h.keys("a")
	assert.Equal(t, tea.MouseModeNone, h.s.View().MouseMode, "no mouse reports while the engine is not on screen")
}

func TestSplit_MailboxPumpHandsTheQueueOver(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.s.driven = false
	h.s.mailbox = newPaneMailbox()
	cb := h.s.mailbox.callbacks()
	cb.Title("one")
	cb.Title("two")
	assert.Equal(t, paneEventsMsg{paneTitleMsg("one"), paneTitleMsg("two")}, h.s.waitPaneEvents()(), "lossless, in order")
	cb.Bell()
	assert.Equal(t, paneEventsMsg{paneBellMsg{}}, h.s.waitPaneEvents()())
}

func TestSplit_FrameRowsAreSerializedOncePerFrame(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 4)
	h.engineWrites("one")
	first := h.s.paneLines
	h.frame()
	assert.Same(t, &first[0], &h.s.paneLines[0], "an unchanged frame keeps its rows")
	h.engineWrites("\x1b[Htwo")
	assert.Equal(t, "two", strings.TrimRight(h.s.paneLines[0], " "))
	assert.NotEqual(t, first[0], h.s.paneLines[0])
}

func TestSplit_FocusReportsFollowTheHumansTerminal(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 4)
	h.engineWrites("\x1b[?1004h")
	h.typeBytes("\x1b[I")
	h.engine.waitFor(t, "\x1b[I")
	h.typeBytes("\x1b[O")
	h.engine.waitFor(t, "\x1b[O")
	assert.True(t, h.s.View().ReportFocus, "the split asks the human's terminal for focus reports")
}

// Approvals waiting, the arrival bell (rate-limited, silent while the view
// is open), a ring, and a timed note.
func TestSplit_ChromeCountsBellsAndNotes(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 12)
	h.send(approvalsCountMsg{n: 2, oldest: h.clk.Now().Add(-3 * time.Minute), arrived: true})
	assert.True(t, h.panelHas("⚑ 2 approvals · 3m · a"))
	assert.Equal(t, []string{"\a"}, h.raws())
	h.advance(approvalBellInterval - time.Second)
	h.send(approvalsCountMsg{n: 3, arrived: true})
	assert.Len(t, h.raws(), 1, "a burst rings once")
	h.advance(time.Second)
	h.send(approvalsCountMsg{n: 1, arrived: true})
	assert.Len(t, h.raws(), 2)
	assert.True(t, h.panelHas("⚑ 1 approval · a"))
	h.send(ringMsg{})
	assert.Len(t, h.raws(), 3, "a ring always rings")

	h.send(noteMsg{text: "rate limited until 12:00", d: 15 * time.Second})
	assert.True(t, h.panelHas("rate limited"))
	h.send(noteMsg{text: "second note", d: 15 * time.Second})
	h.advance(10 * time.Second)
	h.send(noteMsg{text: "third note", d: 15 * time.Second})
	h.advance(5 * time.Second)
	assert.True(t, h.panelHas("third note"), "an older note's expiry leaves a newer one")
	h.advance(10 * time.Second)
	assert.False(t, h.panelHas("third note"))
}
