package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
)

// fenceKeyMsg is FenceKey(n) as bubbletea's input parser delivers it.
func fenceKeyMsg(n int) tea.KeyPressMsg { return tea.KeyPressMsg{Code: fenceKeyBase + rune(n)} }

// raws runs cmd's tree and collects every tea.RawMsg it produces, in order;
// whatever else it produces is dropped.
func raws(cmd tea.Cmd) []string {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case tea.RawMsg:
		return []string{msg.Msg.(string)}
	case tea.BatchMsg:
		var out []string
		for _, c := range msg {
			out = append(out, raws(c)...)
		}
		return out
	}
	return nil
}

// A fence waits for the background commands in flight when it arrived — the
// roster fetch here — and for those their results start in turn — the first
// roster's feed open — and only then asks for its token to be written.
func TestFence_WaitsForBackgroundCommandsInFlight(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "perky-same-chevy", State: "live"})
	m := newTestModel(f)
	batch, ok := m.Init()().(tea.BatchMsg)
	require.True(t, ok, "Init batches the roster fetch and its tick")
	fetch := batch[0] // the roster fetch, then its tick

	next, cmd := m.Update(fenceKeyMsg(1))
	m = next.(Model)
	assert.Nil(t, cmd, "the roster fetch is still in flight, so the fence is held")

	next, open := m.Update(fetch())
	m = next.(Model)
	assert.Equal(t, []RosterRow{{Harp: "perky-same-chevy", State: "live"}}, m.rows, "the fetch's result is applied")
	require.NotNil(t, open, "the first roster opens its feed")
	opened := open()
	require.IsType(t, bgDoneMsg{}, opened, "the feed open is in flight, so the fence is still held")

	next, cmd = m.Update(opened)
	m = next.(Model)
	// The open's result also arms the feed's event wait, which raws runs:
	// end the feed so that wait returns.
	f.mu.Lock()
	close(f.events["perky-same-chevy"])
	f.mu.Unlock()
	assert.Equal(t, []string{fenceToken(1)}, raws(cmd), "the feed open's result releases the held fence")
	assert.NotNil(t, m.feed, "and the result itself is applied")

	_, cmd = m.Update(fenceWriteMsg(1))
	assert.Equal(t, []string{FenceMarker(1)}, raws(cmd), "the token's flush writes the marker")
}

// A fence is not a key the human pressed: the presentation chord is still
// the next key's.
func TestFence_LeavesTheFirstKeyChord(t *testing.T) {
	m := newTestModel(newFakeSources(t.TempDir()))
	next, _ := m.Update(fenceKeyMsg(1))
	next, _ = next.(Model).Update(tea.KeyPressMsg{Code: 'f', Text: "f"})
	assert.True(t, next.(Model).full, "f after a fence is still prefix-then-f")
}

// The writer bubbletea draws through strips a fence token and reports it,
// and passes everything else on.
func TestOnlcr_StripsAFenceTokenAndReportsIt(t *testing.T) {
	var out bytes.Buffer
	var got []int
	w := onlcr{w: &out, fenced: func(n int) { got = append(got, n) }}
	p := "a\n" + fenceToken(3) + "b" + fenceToken(4)
	n, err := w.Write([]byte(p))
	require.NoError(t, err)
	assert.Equal(t, len(p), n)
	assert.Equal(t, "a\r\nb", out.String())
	assert.Equal(t, []int{3, 4}, got)
}

// Through a REAL program: a fence typed while the roster fetch is held is
// answered only after the frame painting that roster, and a key typed
// before a fence is in the frame the marker follows.
func TestOverlay_FenceMarkerFollowsTheFrameOfEverythingBeforeIt(t *testing.T) {
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "perky-same-chevy", State: "live"})
	f.rosterGate = make(chan struct{})
	o := NewOverlay(context.Background(), f.sources(), 0x1d, termui.OverlayStart{})
	pr, pw := io.Pipe()
	defer pw.Close()
	var tty syncBuffer
	done := make(chan error, 1)
	go func() { done <- o.Run(pr, &tty, testGeo()) }()

	waitForOverlay(t, "first frame painted", func() bool { return strings.Contains(tty.String(), statusLoading) })
	_, err := io.WriteString(pw, FenceKey(1))
	require.NoError(t, err)
	close(f.rosterGate)
	waitForOverlay(t, "the first fence's marker", func() bool { return strings.Contains(tty.String(), FenceMarker(1)) })
	out := tty.String()
	assert.Less(t, strings.Index(out, "perky-same-chevy"), strings.Index(out, FenceMarker(1)),
		"the roster's frame is written before the fence that waited for it")

	_, err = io.WriteString(pw, "f"+FenceKey(2))
	require.NoError(t, err)
	waitForOverlay(t, "the second fence's marker", func() bool { return strings.Contains(tty.String(), FenceMarker(2)) })
	out = tty.String()
	enter := strings.Index(out, "\x1b[?1049h")
	require.GreaterOrEqual(t, enter, 0, "f entered full screen")
	assert.Less(t, enter, strings.Index(out, FenceMarker(2)), "the key's frame is written before the fence typed after it")

	_, err = io.WriteString(pw, "q")
	require.NoError(t, err)
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after q")
	}
}
