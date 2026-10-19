package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/frontend"
)

// paneText is the split's right pane, styles stripped.
func paneText(h *splitHarness) []string {
	var out []string
	for _, r := range h.screen() {
		parts := strings.SplitN(r, "│", 2)
		if len(parts) == 2 {
			out = append(out, strings.TrimRight(parts[1], " "))
		}
	}
	return out
}

func childHarness(t *testing.T) (*splitHarness, *splitPort) {
	t.Helper()
	port := newSplitPort()
	port.child("brave-heron", "coder", selfHarp, coord.StateExecuting)
	port.child("sleepy-ibis", "reviewer", selfHarp, coord.StateIdle)
	h := newSplitHarness(t, port).sized(100, 10)
	h.engineWrites("ENGINE SCREEN")
	return h, port
}

// Selecting a child shows its feed in place of the engine; selecting the
// session's own row brings the engine back, releasing the feed.
func TestSplitChild_SelectingAChildShowsItsFeed(t *testing.T) {
	h, port := childHarness(t)
	require.Equal(t, "ENGINE SCREEN", paneText(h)[0])
	h.typeBytes("\x1d")
	h.keys("j")
	assert.Equal(t, []string{"brave-heron"}, port.watchedHarps())
	h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "hello from the child")})
	h.send(feedEventMsg{harp: "brave-heron", ev: frontend.FeedEvent{Gap: 2}})
	pane := paneText(h)
	assert.Equal(t, " feed: brave-heron (coder) · live · ▼ follow", pane[0])
	assert.Contains(t, strings.Join(pane, "\n"), "asst  < hello from the child")
	assert.Contains(t, strings.Join(pane, "\n"), "2 live events dropped")
	assert.Contains(t, pane[len(pane)-1], "send → brave-heron:")
	assert.NotContains(t, strings.Join(pane, "\n"), "ENGINE SCREEN")

	h.keys("j")
	assert.Equal(t, "brave-heron", port.released(t), "moving on releases the previous feed")
	assert.Equal(t, []string{"brave-heron", "sleepy-ibis"}, port.watchedHarps())
	h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "late")})
	assert.NotContains(t, strings.Join(paneText(h), "\n"), "late", "a feed event for a harp no longer viewed is dropped")

	h.keys("k", "k")
	assert.Equal(t, "ENGINE SCREEN", paneText(h)[0])
	// Back through brave-heron (opened again, then let go) to the engine;
	// each release lands on its own goroutine, in either order.
	assert.ElementsMatch(t, []string{"sleepy-ibis", "brave-heron"}, []string{port.released(t), port.released(t)})
	assert.Equal(t, []string{"brave-heron", "sleepy-ibis", "brave-heron"}, port.watchedHarps())
}

// Typing into a child's line sends through the port's Control: Enter as the
// human's send (a steer the child takes at its next turn), alt+Enter as a
// steer that interrupts its turn in flight.
func TestSplitChild_TypingSendsThroughControl(t *testing.T) {
	h, port := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j", "esc")
	require.Equal(t, focusPane, h.s.focus)
	h.typeBytes("fix the test")
	h.keys("backspace")
	assert.Contains(t, paneText(h)[len(paneText(h))-1], "send → brave-heron: fix the tes")
	c := h.s.View().Cursor
	require.NotNil(t, c)
	assert.Equal(t, rosterPaneWidth+1+len(" send → brave-heron: fix the tes")-2, c.X, "the cursor ends the typed text (the arrow is one column)")
	assert.Equal(t, 9, c.Y)

	h.keys("enter")
	h.typeBytes("stop now")
	h.keys("alt+enter")
	h.keys("enter") // an empty line sends nothing
	assert.Equal(t, []coord.ControlRequest{
		{Verb: coord.ControlVerbSteer, Harp: "brave-heron", Body: "fix the tes"},
		{Verb: coord.ControlVerbSteer, Harp: "brave-heron", Body: "stop now", Interrupt: true},
	}, port.controlled())
	assert.True(t, h.panelHas("injected into brave-heron: queued"))
	assert.Empty(t, h.engine.String(), "nothing typed for the child reached the engine")

	h.typeBytes("draft")
	h.keys("esc")
	assert.Empty(t, h.s.child.text, "esc clears the line")
}

// The tree's control keys act on its selected child: pause and resume at
// once, worded as the overlay words them; ? and s latch the input line's verb
// and hand it focus. Interrupt is a steer's alone.
func TestSplitChild_TreeControlKeys(t *testing.T) {
	h, port := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("i")
	assert.True(t, h.panelHas("select a child"), "the engine's own row takes no control verb")
	h.keys("j", "p")
	assert.True(t, h.panelHas("paused brave-heron"))
	h.keys("r")
	h.keys("?")
	assert.Equal(t, focusPane, h.s.focus)
	assert.Contains(t, paneText(h)[len(paneText(h))-1], "ask → brave-heron:")
	h.typeBytes("status?")
	h.keys("alt+enter")
	h.typeBytes("\x1d")
	h.keys("s")
	h.typeBytes("the diff")
	h.keys("enter")
	h.typeBytes("\x1d")
	h.keys("i")
	h.typeBytes("go on")
	h.keys("enter")
	assert.Equal(t, []coord.ControlRequest{
		{Verb: coord.ControlVerbPause, Harp: "brave-heron"},
		{Verb: coord.ControlVerbResume, Harp: "brave-heron"},
		{Verb: coord.ControlVerbQuestion, Harp: "brave-heron", Body: "status?"},
		{Verb: coord.ControlVerbSummarize, Harp: "brave-heron", Body: "the diff"},
		{Verb: coord.ControlVerbSteer, Harp: "brave-heron", Body: "go on"},
	}, port.controlled())
	assert.Contains(t, paneText(h)[len(paneText(h))-1], "send → brave-heron:", "the verb goes back to a send after each")
}

func TestSplitChild_ControlFailureIsTheNote(t *testing.T) {
	h, port := childHarness(t)
	port.controlFn = func(coord.ControlRequest) (coord.ControlResult, error) {
		return coord.ControlResult{}, coord.ErrNotInjectable
	}
	h.typeBytes("\x1d")
	h.keys("j", "p")
	assert.True(t, h.panelHas("pause brave-heron:"))

	noPort := newSplitHarness(t, nil).sized(100, 10)
	noPort.send(rosterMsg{rows: []RosterRow{{Harp: selfHarp}, {Harp: "brave-heron", Parent: selfHarp, Depth: 1}}})
	noPort.typeBytes("\x1d")
	noPort.keys("j", "p")
	assert.True(t, noPort.panelHas("unavailable"))
}

func TestSplitChild_FeedLifecycleNotes(t *testing.T) {
	h, _ := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j")
	h.send(feedClosedMsg{harp: "brave-heron"})
	assert.True(t, h.panelHas("feed ended (agent exited)"))
	h.send(feedClosedMsg{harp: "brave-heron", err: errors.New("watch broke")})
	assert.True(t, h.panelHas("feed ended: watch broke"))
	h.send(feedErrMsg{harp: "brave-heron", err: errors.New("no transcript")})
	assert.True(t, h.panelHas("feed brave-heron: no transcript"))
	h.send(feedErrMsg{harp: "someone-else", err: errors.New("not mine")})
	assert.False(t, h.panelHas("not mine"))

	// An open that lands after the selection moved on is released.
	stale := &Feed{Source: "live", Events: make(chan frontend.FeedEvent), Cancel: func() { h.s.status = "stale released" }}
	h.send(feedOpenedMsg{harp: "someone-else", feed: stale})
	assert.Equal(t, "stale released", h.s.status)
}

// The feed follows its tail; the paging keys scroll back, no further than
// its top, and End follows again.
func TestSplitChild_FeedScrolls(t *testing.T) {
	h, _ := childHarness(t)
	h.typeBytes("\x1d")
	h.keys("j", "esc")
	for i := range 20 {
		h.send(feedEventMsg{harp: "brave-heron", ev: entryEv("assistant", "line "+string(rune('a'+i)))})
	}
	body := func() []string { p := paneText(h); return p[1 : len(p)-1] }
	assert.Contains(t, body()[len(body())-1], "line t", "following the tail")
	h.keys("up")
	assert.Contains(t, body()[len(body())-1], "line s")
	assert.Contains(t, paneText(h)[0], "brave-heron (coder) · live", "scrolled back: no longer following")
	assert.NotContains(t, paneText(h)[0], "follow")
	h.keys("pgup", "pgup", "pgup")
	assert.Contains(t, body()[0], "line a", "no further than the top")
	h.keys("pgdown")
	assert.Contains(t, body()[0], "line k", "a page back down: twelve above the tail less ten")
	h.keys("down")
	assert.Contains(t, body()[0], "line l")
	h.keys("end")
	assert.Contains(t, body()[len(body())-1], "line t")
}
