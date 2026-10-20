package tui

import (
	"context"
	"errors"
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
	"github.com/ctxloom/ctxloom/internal/testsupport/frontendfake"
)

// The timings and widths the split view is specified by, stated as numbers
// so a changed constant is a changed behaviour.
func TestSplit_TheNumbers(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.send(tea.WindowSizeMsg{Width: 66, Height: 10})
	panelW, paneW := h.s.layout()
	assert.Equal(t, [2]int{2, 63}, [2]int{panelW, paneW}, "66 columns: the glyph column")
	h.send(tea.WindowSizeMsg{Width: 67, Height: 10})
	panelW, paneW = h.s.layout()
	assert.Equal(t, [2]int{26, 40}, [2]int{panelW, paneW}, "67 columns: the tree and a 40-column pane")

	h.advance(199 * time.Millisecond)
	assert.Empty(t, h.engine.resizes(), "the colours are waited for 200ms")
	h.advance(time.Millisecond)
	assert.Equal(t, [][2]uint16{{10, 40}}, h.engine.resizes())

	h.send(rosterEventsMsg{})
	reads := h.port.runReads()
	h.advance(249 * time.Millisecond)
	assert.Equal(t, reads, h.port.runReads(), "events are gathered for 250ms")
	h.advance(time.Millisecond)
	assert.Equal(t, reads+1, h.port.runReads())

	h.send(approvalsCountMsg{n: 1, arrived: true})
	h.advance(9*time.Second + 999*time.Millisecond)
	h.send(approvalsCountMsg{n: 2, arrived: true})
	h.advance(time.Millisecond)
	h.send(approvalsCountMsg{n: 3, arrived: true})
	assert.Len(t, h.raws(), 2, "the arrival bell rings at most every 10s")
}

func TestSplit_SafetyTickIsFiveSeconds(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	reads := h.port.runReads()
	h.advance(4*time.Second + 999*time.Millisecond)
	assert.Equal(t, reads, h.port.runReads())
	h.advance(time.Millisecond)
	assert.Equal(t, reads+1, h.port.runReads())
}

func TestSplit_DozingIsTenMinutesIdle(t *testing.T) {
	now := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)
	r := RosterRow{State: coord.StateIdle, LastActivity: now.Add(-10 * time.Minute)}
	assert.Equal(t, "○", activityGlyph(r, now))
	r.LastActivity = now.Add(-10*time.Minute + time.Nanosecond)
	assert.Equal(t, "◐", activityGlyph(r, now))
}

func TestCompactCount_Boundaries(t *testing.T) {
	for _, c := range []struct {
		n    uint64
		want string
	}{
		{999, "999"}, {1000, "1k"}, {1999, "1k"}, {999_999, "999k"}, {1_000_000, "1.0M"},
		{1_049_999, "1.0M"}, {1_050_000, "1.1M"},
	} {
		assert.Equal(t, c.want, compactCount(c.n), "%d", c.n)
	}
	assert.Equal(t, "100%", tokenLabel(RosterRow{ContextTokens: 1, ContextWindow: 1}), "a window of one is a window")
}

func TestWindowStart(t *testing.T) {
	for _, c := range []struct{ sel, n, h, want int }{
		{0, 3, 5, 0}, {2, 3, 5, 0}, {-1, 9, 4, 0}, {0, 9, 4, 0}, {3, 9, 4, 1},
		{4, 9, 4, 2}, {8, 9, 4, 5}, {5, 5, 5, 0}, {7, 9, 3, 6}, {4, 9, 1, 4},
	} {
		assert.Equal(t, c.want, windowStart(c.sel, c.n, c.h), "sel %d of %d in %d", c.sel, c.n, c.h)
	}
}

func TestRealClock(t *testing.T) {
	assert.WithinDuration(t, time.Now(), RealClock{}.Now(), time.Second)
	fired := make(chan struct{})
	stop := RealClock{}.AfterFunc(time.Hour, func() { close(fired) })
	assert.True(t, stop(), "stopped before it fired")
	RealClock{}.AfterFunc(0, func() { close(fired) })
	<-fired
}

func TestSplit_WidthMethodSwitchRepaints(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	_, cmd := h.s.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeSet})
	require.NotNil(t, cmd)
	assert.Equal(t, tea.ClearScreen(), cmd())
	_, cmd = h.s.Update(tea.ModeReportMsg{Mode: ansi.ModeFocusEvent, Value: ansi.ModeSet})
	assert.Nil(t, cmd, "another mode's report is not a repaint")
}

// The panel gives way, on a short terminal, header first and then the
// footer's oldest lines; the tree keeps a row.
func TestSplit_PanelOnAShortTerminal(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	panel := func(rows int) []string {
		var out []string
		for _, l := range h.s.panelLines(rosterPaneWidth, rows) {
			out = append(out, strings.TrimSpace(ansi.Strip(l)))
		}
		return out
	}
	assert.Equal(t, []string{selfHarp, "claude-code·haiku", "● " + selfHarp, "^] agents"}, panel(4))
	assert.Equal(t, []string{selfHarp, "● " + selfHarp, "^] agents"}, panel(3))
	assert.Equal(t, []string{"● " + selfHarp, "^] agents"}, panel(2))
	assert.Equal(t, []string{"● " + selfHarp}, panel(1))
	assert.Empty(t, panel(0))
	h.typeBytes("\x1d")
	tall := panel(10)
	short := panel(4)
	assert.Equal(t, "● "+selfHarp, short[0], "the tree's row survives")
	assert.Equal(t, tall[len(tall)-3:], short[1:], "the footer keeps its newest lines")
}

func TestSplit_HeaderNamesWhatItCan(t *testing.T) {
	h := newSplitHarness(t, newSplitPort(), func(o *SplitOptions) { o.Header = Header{} }).sized(100, 10)
	head := func() []string {
		var out []string
		for _, l := range h.s.headerLines(rosterPaneWidth) {
			out = append(out, strings.TrimSpace(ansi.Strip(l)))
		}
		return out
	}
	assert.Equal(t, []string{selfHarp}, head(), "no header: the session's own harp")
	h.s.header = Header{Harp: "named", Agent: "coder", Model: "haiku"}
	assert.Equal(t, []string{"named·coder", "haiku"}, head())
	h.s.header = Header{Harp: "named", Engine: "codex"}
	assert.Equal(t, []string{"named", "codex"}, head())
}

func TestSplit_TreeRowAnnotations(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	h.send(rosterMsg{rows: []RosterRow{
		{Harp: selfHarp},
		{Harp: "a", Depth: 1, State: StateEnded, Cause: coord.CauseFinalReported, Unread: 1},
		{Harp: "b", Depth: 1, State: coord.StateIdle, Unread: 0, ContextTokens: 5, ContextWindow: 10},
	}})
	tree := h.s.treeLines(rosterPaneWidth, 3)
	assert.Equal(t, " ▾◐ "+selfHarp, strings.TrimRight(ansi.Strip(tree[0]), " "))
	assert.Equal(t, "  ✓ a final-reported ✉ 1", strings.TrimRight(ansi.Strip(tree[1]), " "))
	assert.Equal(t, "  ◐ b"+strings.Repeat(" ", 18)+"50%", ansi.Strip(tree[2]), "the gauge right-aligned, the row the panel's width")
}

// A parent whose only child is the tree's last row folds.
func TestSplit_FoldTheLastSubtree(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	h.send(rosterMsg{rows: []RosterRow{{Harp: selfHarp}, {Harp: "p", Depth: 1}, {Harp: "c", Depth: 2}}})
	assert.Contains(t, h.s.treeLines(rosterPaneWidth, 3)[1], foldOpen)
	h.typeBytes("\x1d")
	h.keys("j", "enter")
	assert.Equal(t, []int{0, 1}, h.s.visibleRows())
	assert.Contains(t, h.s.treeLines(rosterPaneWidth, 3)[1], foldClosed)
	h.keys("enter")
	assert.Equal(t, []int{0, 1, 2}, h.s.visibleRows())
}

// With no row selected the tree's j starts from the top; with no rows it
// does nothing.
func TestSplit_MovingFromNoSelection(t *testing.T) {
	h := newSplitHarness(t, nil, func(o *SplitOptions) { o.Self = "" }).sized(100, 10)
	h.typeBytes("\x1d")
	h.keys("j")
	assert.Equal(t, "", h.s.sel, "no rows: nothing to move to")
	h.send(rosterMsg{rows: []RosterRow{{Harp: "x"}, {Harp: "y"}}})
	h.keys("j")
	assert.Equal(t, "x", h.s.sel, "from no selection, j lands on the top row")
	h.keys("j", "k")
	assert.Equal(t, "x", h.s.sel, "k back to the top row")
}

func TestSplit_ZeroSizeIsNoSize(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	h.send(tea.WindowSizeMsg{Width: 0, Height: 0})
	h.send(tea.WindowSizeMsg{Width: 100, Height: 0})
	h.send(tea.WindowSizeMsg{Width: 0, Height: 10})
	assert.Equal(t, [2]int{100, 10}, [2]int{h.s.w, h.s.h})
	assert.Len(t, h.engine.resizes(), 1)
}

// Colours known before any size: the engine waits for one.
func TestSplit_ColoursBeforeASize(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.advance(time.Second)
	h.send(tea.BackgroundColorMsg{})
	assert.Empty(t, h.engine.resizes())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 10})
	assert.Equal(t, [][2]uint16{{10, 73}}, h.engine.resizes())
	h.send(tea.WindowSizeMsg{Width: 100, Height: 12})
	assert.Equal(t, [2]uint16{12, 73}, h.engine.resizes()[1], "a height change alone resizes")
}

// Between a resize and its frame, the pane shows the old frame fitted to the
// new size.
func TestSplit_OldFrameFittedUntilTheNewOne(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	h.engineWrites("old")
	h.send(tea.WindowSizeMsg{Width: 100, Height: 12})
	rows := h.s.rightLines(73, 12)
	require.Len(t, rows, 12)
	assert.Equal(t, strings.Repeat(" ", 73), rows[11])
	h.send(tea.WindowSizeMsg{Width: 90, Height: 12})
	rows = h.s.rightLines(63, 12)
	assert.Equal(t, 63, ansi.StringWidth(rows[0]), "a narrower pane cuts the old frame")
}

// stubPane is an emulator whose frames carry no cells.
type stubPane struct{ termemu.Terminal }

func (stubPane) Frame() termemu.Frame { return termemu.Frame{Seq: 7} }

func TestSplit_AFrameWithNoCells(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 4)
	h.s.pane = stubPane{h.s.pane}
	h.frame()
	assert.Nil(t, h.s.paneLines)
	assert.Equal(t, strings.Repeat(" ", 73), h.s.rightLines(73, 4)[0])
}

func TestSplit_TheTreeTakesNoBannerAway(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.typeBytes("\x1d")
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, notice: "approval from brave-heron", reply: reply})
	require.ErrorIs(t, <-reply, ErrEngaged)
	assert.True(t, h.panelHas("approval from brave-heron"))
	h.typeBytes("\x1d")
	assert.True(t, h.panelHas("approval from brave-heron"), "back to the pane, the banner stays")
	h.typeBytes("\x1d")
	assert.False(t, h.panelHas("approval from brave-heron"), "taking the tree clears it")
}

func TestSplit_APasteIsInputForTheQuietGate(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.advance(time.Hour)
	h.send(tea.PasteMsg{Content: "pasted"})
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, reply: reply})
	assert.Empty(t, reply, "a paste just now: not quiet")
	assert.Equal(t, "pasted", h.settleEngine(t))
}

func TestSplit_NoMouseIntoTheEngineUnderTheApprovals(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.engineWrites("\x1b[?1000h\x1b[?1006h")
	h.typeBytes("\x1d")
	h.keys("a")
	h.typeBytes("\x1b[<0;40;5M")
	assert.Empty(t, h.settleEngine(t))
	assert.Nil(t, h.s.View().Cursor, "no cursor while the approvals view is open")
}

func TestSplit_ChildPaneAsksForNoMouse(t *testing.T) {
	h, _ := childHarness(t)
	h.send(paneEventsMsg{paneMouseMsg(termemu.MouseClicks)})
	assert.Equal(t, tea.MouseModeCellMotion, h.s.View().MouseMode)
	h.typeBytes("\x1d")
	h.keys("j")
	assert.Equal(t, tea.MouseModeNone, h.s.View().MouseMode)
}

func TestSplit_HiddenEngineCursor(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	h.engineWrites("\x1b[?25l")
	assert.Nil(t, h.s.View().Cursor)
	h.engineWrites("\x1b[?25h")
	assert.NotNil(t, h.s.View().Cursor)
}

func TestSplit_NoCursorBeforeASize(t *testing.T) {
	h := newSplitHarness(t, newSplitPort())
	h.send(rosterMsg{rows: []RosterRow{{Harp: selfHarp}, {Harp: "kid", Parent: selfHarp, Depth: 1}}})
	h.s.sel = "kid"
	assert.Nil(t, h.s.cursor(), "a child's input line has no row to sit on yet")
}

// A summoned approval takes the pane without moving focus off it, and the
// engine's cursor goes with the pane: none blinks over the approval.
func TestSplit_NoCursorOverASummonedApproval(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	require.NotNil(t, h.s.View().Cursor, "the engine's cursor shows before the summon")
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, notice: "approval from brave-heron", reply: reply})
	require.NoError(t, <-reply)
	require.Equal(t, focusPane, h.s.focus)
	assert.Nil(t, h.s.View().Cursor)
}

// A one-row terminal is a size: the cursor sits on its only row.
func TestSplit_CursorOnAOneRowTerminal(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 1)
	require.NotNil(t, h.s.View().Cursor)
	assert.Zero(t, h.s.View().Cursor.Y)
}

func TestSplit_SummonWhileTheTreesApprovalsAreOpen(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.typeBytes("\x1d")
	h.keys("a")
	require.True(t, h.s.apprOpen)
	h.s.focus = focusPane // engaged by the open view alone
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, notice: "n", reply: reply})
	assert.ErrorIs(t, <-reply, ErrEngaged)
}

// A retry or a withdrawal for another summon leaves the waiting one alone.
func TestSplit_StaleSummonMessagesAreIgnored(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.typeBytes("x")
	reply := make(chan error, 1)
	h.send(summonMsg{id: 5, reply: reply})
	h.send(summonCancelMsg{id: 4})
	require.NotNil(t, h.s.summon, "another summon's withdrawal")
	h.advance(time.Hour)
	assert.NoError(t, <-reply)
	h.send(summonRetryMsg{id: 5})
	assert.Nil(t, h.s.summon)

	h2 := newSplitHarness(t, port).sized(100, 30)
	h2.typeBytes("x")
	reply2 := make(chan error, 1)
	h2.send(summonMsg{id: 1, reply: reply2})
	h2.send(summonRetryMsg{id: 2})
	assert.Empty(t, reply2, "a retry for another summon is not this one's attempt")
	assert.False(t, h2.s.apprOpen)
}

// An inert window's check left over from a view that closed never arms the
// next one early (it can outlive the view when ArmFor exceeds QuietFor).
func TestSplit_AStaleArmCheckArmsNothing(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port, func(o *SplitOptions) {
		o.Present = PresentPolicy{QuietFor: 500 * time.Millisecond, ArmFor: 2 * time.Second}
	}).sized(100, 30)
	reply := make(chan error, 1)
	h.send(summonMsg{id: 1, reply: reply})
	require.NoError(t, <-reply)
	h.advance(time.Second)
	h.s.closeApprovals()
	h.send(summonMsg{id: 2, reply: reply})
	require.NoError(t, <-reply)
	h.advance(time.Second) // the first view's check comes due
	assert.True(t, h.s.arming, "the first view's check armed the second")
	h.advance(time.Second)
	assert.False(t, h.s.arming, "the second arms on its own ArmFor")
}

// Opening the view starts the queue's one pump and the countdown tick.
func TestSplit_OpeningStartsThePumpAndTheTick(t *testing.T) {
	port := newSplitPort()
	ch := make(chan coord.QueueEvent, 1)
	q := &pumpQueue{fakeQueue: newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour)), ch: ch,
		extra: bashReq("a2", "sleepy-ibis", "pwd", 2*time.Hour)}
	port.queue = q
	h := newSplitHarness(t, port).sized(100, 30)
	ch <- coord.QueueEvent{Kind: coord.QueueAdded, ID: "a2"}
	close(ch)
	h.typeBytes("\x1d")
	h.keys("a")
	assert.Len(t, h.s.appr.rows, 2, "the pump delivered the arrival")
	ticks := 0
	for _, m := range h.out {
		if _, ok := m.(approvalsTickMsg); ok {
			ticks++
		}
	}
	assert.Equal(t, 1, ticks, "the countdown ticks")
}

// pumpQueue's subscription carries what the test queued on ch, and its
// queue holds extra from its second read on: what an arrival announces.
type pumpQueue struct {
	*fakeQueue
	ch    chan coord.QueueEvent
	reads int
	extra coord.PendingApproval
}

func (q *pumpQueue) Pending() []coord.PendingApproval {
	q.reads++
	out := q.fakeQueue.Pending()
	if q.reads > 1 {
		out = append(out, q.extra)
	}
	return out
}

func (q *pumpQueue) Subscribe(context.Context) <-chan coord.QueueEvent { return q.ch }

// The approvals view in the split takes pastes into its note field, as the
// modal does, and only Enter on Deny denies.
func TestSplit_ApprovalsTakeAPasteIntoTheNote(t *testing.T) {
	port := newSplitPort()
	q := newFakeQueue(bashReq("a", "wiry-otter", "rm -rf /", 5*time.Minute))
	port.queue = q
	h := newSplitHarness(t, port).sized(110, 40)
	h.typeBytes("\x1d")
	h.keys("a")
	for _, k := range []string{"right", "right", "right", "enter", "tab"} {
		h.send(apprKey(k))
	}
	h.keys("n", "o")
	h.send(tea.PasteMsg{Content: " — use the sandbox"})
	assert.Empty(t, q.calls())
	h.keys("enter")
	require.Len(t, q.calls(), 1)
	assert.Equal(t, coord.ApprovalDecision{Message: "no — use the sandbox"}, q.calls()[0].d)
}

func TestSplit_ACountWithoutAnArrivalRingsNothing(t *testing.T) {
	port := newSplitPort()
	port.queue = newFakeQueue(bashReq("a1", "brave-heron", "ls", time.Hour))
	h := newSplitHarness(t, port).sized(100, 30)
	h.send(approvalsCountMsg{n: 1})
	assert.Empty(t, h.raws())
	h.typeBytes("\x1d")
	h.keys("a")
	h.send(approvalsCountMsg{n: 2, arrived: true})
	assert.Empty(t, h.raws(), "the open view is its own signal")
}

// The child pane: its feed pump re-arms per event and stops with the feed;
// an event after the feed closed, or for another harp's close, changes
// nothing.
func TestSplit_ChildFeedPump(t *testing.T) {
	h, port := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j")
	h.s.driven = false
	ch := port.feeds["brave-heron"]
	ch <- entryEv("assistant", "first")
	cmd := h.s.waitFeedCmd()
	require.NotNil(t, cmd)
	msg := cmd()
	assert.Equal(t, feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "first")}, msg)
	_, next := h.s.Update(msg)
	assert.NotNil(t, next, "the pump re-arms")
	h.s.driven = true

	h.send(feedClosedMsg{harp: "someone-else", err: errors.New("theirs")})
	assert.NotNil(t, h.s.child.feed, "another harp's close")
	assert.False(t, h.panelHas("theirs"))
	h.send(feedClosedMsg{harp: "brave-heron"})
	h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "after the end")})
	assert.NotContains(t, strings.Join(paneText(h), "\n"), "after the end")
	h.s.driven = false
	assert.Nil(t, h.s.waitFeedCmd(), "no feed, no pump")
}

func TestSplit_ChildPaneSwitchForgetsThePrevious(t *testing.T) {
	h, _ := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j")
	h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "brave's line")})
	h.keys("esc")
	h.typeBytes("draft")
	h.typeBytes("\x1d")
	h.keys("j")
	pane := strings.Join(paneText(h), "\n")
	assert.NotContains(t, pane, "brave's line")
	assert.NotContains(t, pane, "draft")
	assert.NotContains(t, pane, "▸", "no item cursor in a child's feed")
}

func TestSplit_ChildInputEdges(t *testing.T) {
	h, port := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j", "esc")
	h.keys("backspace")
	assert.Empty(t, h.s.child.text, "backspace on an empty line")
	h.keys("down", "pgdown")
	assert.Zero(t, h.s.child.scroll, "already at the tail")
	h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "short")})
	h.keys("pgup")
	assert.Zero(t, h.s.child.scroll, "a feed that fits does not scroll")
	assert.Contains(t, paneText(h)[0], "follow")

	h.typeBytes("\x1d")
	h.keys("?")
	h.keys("esc")
	assert.Contains(t, paneText(h)[len(paneText(h))-1], "send → brave-heron:", "esc drops a latched verb")
	h.typeBytes("\x1d")
	h.keys("?")
	h.typeBytes("q")
	h.keys("enter")
	assert.Contains(t, paneText(h)[len(paneText(h))-1], "send → brave-heron:", "a sent question leaves a send")
	assert.Equal(t, coord.ControlVerbQuestion, port.controlled()[0].Verb)
}

func TestSplit_ControlPendingThenOutcome(t *testing.T) {
	h, port := childHarness(t)
	port.controlFn = func(coord.ControlRequest) (coord.ControlResult, error) {
		return coord.ControlResult{}, errors.New("refused")
	}
	cmd := h.s.sendControl(coord.ControlRequest{Verb: coord.ControlVerbPause, Harp: "brave-heron"})
	assert.Equal(t, "pausing brave-heron…", h.s.status)
	h.send(cmd())
	assert.Equal(t, "pause brave-heron: refused", h.s.errMsg)
	assert.Empty(t, h.s.status)
}

func TestSplit_RosterReadsFail(t *testing.T) {
	port := newSplitPort()
	h := newSplitHarness(t, port).sized(100, 10)
	failing := &frontendfake.Port{
		ListSessionsFn: func(context.Context) ([]frontend.Session, error) { return nil, errors.New("index unreadable") },
	}
	h.s.port = failing
	h.run(h.s.snapshotRosterCmd(coord.RunsSnapshot{}))
	assert.True(t, h.panelHas("roster: index unreadable"))
	h.s.rosterErr = ""
	h.run(h.s.loadRosterCmd())
	assert.True(t, h.panelHas("roster: index unreadable"))
	h.s.port = nil
	h.run(h.s.openFeedCmd("gone"))
	assert.False(t, h.panelHas("feed gone:"), "not the child under view")
	h.s.child.harp = "gone"
	h.run(h.s.openFeedCmd("gone"))
	assert.True(t, h.panelHas("feed gone:"))
}

func TestSplit_RosterKeepsTheSelectedChild(t *testing.T) {
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	h := newSplitHarness(t, port).sized(100, 10)
	h.typeBytes("\x1d")
	h.keys("j")
	h.send(rosterMsg{rows: []RosterRow{{Harp: selfHarp}, {Harp: "new-one", Depth: 1}, {Harp: "brave-heron", Depth: 1}}})
	assert.Equal(t, "brave-heron", h.s.sel)
	assert.NotNil(t, h.s.child.feed)
}

func TestSplit_DebounceRearmsAfterItsRead(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	reads := h.port.runReads()
	h.send(rosterEventsMsg{})
	h.advance(250 * time.Millisecond)
	h.send(rosterEventsMsg{})
	h.advance(250 * time.Millisecond)
	assert.Equal(t, reads+2, h.port.runReads(), "an event after a debounced read arms another")
}

func TestSplit_PumpsRearm(t *testing.T) {
	port := newSplitPort()
	h := newSplitHarness(t, port).sized(100, 10)
	h.s.driven = false
	_, cmd := h.s.Update(rosterEventsMsg{})
	assert.NotNil(t, cmd, "the run-event pump re-arms")
	h.s.mailbox = nil
	assert.Nil(t, h.s.waitPaneEvents(), "no mailbox, no pump")
}

func TestSplit_NoSafetyTickAfterTheEnd(t *testing.T) {
	h := newSplitHarness(t, newSplitPort()).sized(100, 10)
	pending := h.clk.Pending()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.s.ctx = ctx
	h.s.armSafetyTick()
	assert.Equal(t, pending, h.clk.Pending())
	h.s.ctx = context.Background()
	h.s.armSafetyTick()
	assert.Equal(t, pending+1, h.clk.Pending())
}
