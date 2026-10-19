package termemu

import (
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ready(p *XVT) bool {
	select {
	case <-p.Ready():
		return true
	default:
		return false
	}
}

// Ready carries one signal for any number of commits since it was last
// read, and never blocks the writer.
func TestReady_CoalescesCommits(t *testing.T) {
	p, _ := newPane(t, 10, 2)
	assert.True(t, ready(p), "the pane's first frame is a commit")
	assert.False(t, ready(p))
	_, _ = p.Write([]byte("a"))
	_, _ = p.Write([]byte("b"))
	assert.True(t, ready(p))
	assert.False(t, ready(p), "two commits, one signal")
	p.Resize(8, 3)
	assert.True(t, ready(p), "a resize is a commit")
}

func TestFrame_FirstIsTheBlankScreen(t *testing.T) {
	p, _ := newPane(t, 7, 3)
	f := p.Frame()
	require.NotNil(t, f.Buf)
	assert.Equal(t, uint64(1), f.Seq)
	assert.Equal(t, [2]int{7, 3}, [2]int{f.Buf.Width(), f.Buf.Height()})
	assert.True(t, f.CursorVisible)
	assert.Equal(t, uv.Pos(0, 0), f.Cursor)
}

// A frame is copied once per commit: reading it again without a commit is
// the same copy.
func TestFrame_CopiedOncePerCommit(t *testing.T) {
	p, _ := newPane(t, 7, 3)
	_, _ = p.Write([]byte("x"))
	f1, f2 := p.Frame(), p.Frame()
	assert.Same(t, f1.Buf, f2.Buf)
	_, _ = p.Write([]byte("y"))
	assert.NotSame(t, f1.Buf, p.Frame().Buf)
}

func TestWrite_TakesEveryByteAndCommitsOnce(t *testing.T) {
	p, _ := newPane(t, 10, 2)
	before := p.Frame().Seq
	n, err := p.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	_, _ = p.Write([]byte("\x1b[?2026l")) // a close with no frame open
	assert.Equal(t, before+2, p.Frame().Seq, "one commit per write, none for a stray close")
	assert.False(t, p.Syncing())
	p.Resize(12, 3)
	assert.Equal(t, before+3, p.Frame().Seq)
	assert.Equal(t, 12, p.Frame().Buf.Width())
}

// A frame opened inside an open frame changes nothing: the committed frame
// is still the one before the first open, and the timeout runs from it.
func TestSync_NestedOpenKeepsTheFirst(t *testing.T) {
	p, _ := newPane(t, 20, 2)
	clock := time.Unix(1000, 0)
	p.mu.Lock()
	p.now = func() time.Time { return clock }
	p.mu.Unlock()
	_, _ = p.Write([]byte("old"))
	before := p.Frame()
	_, _ = p.Write([]byte("\x1b[?2026h\x1b[Hhalf"))
	clock = clock.Add(SyncTimeout - 100*time.Millisecond)
	_, _ = p.Write([]byte("\x1b[?2026hmore"))
	assert.True(t, p.Syncing(), "a nested open does not close the frame")
	assert.Equal(t, before.Seq, p.Frame().Seq)
	assert.Equal(t, "old\n", frameText(p.Frame()))

	clock = clock.Add(100 * time.Millisecond)
	_, _ = p.Write([]byte("!"))
	assert.True(t, p.Syncing(), "exactly SyncTimeout after the first open: still held")
	clock = clock.Add(time.Nanosecond)
	_, _ = p.Write([]byte("?"))
	assert.False(t, p.Syncing(), "past it, released — timed from the first open")
	assert.Contains(t, frameText(p.Frame()), "halfmore!?")
}

func kittyFlagsOf(t *testing.T, p *XVT, sink *childSink) string {
	t.Helper()
	_, _ = p.Write([]byte("\x1b[?u"))
	return sink.waitFor(t, "u")
}

// The kitty keyboard stack: push, pop (one by default, never below empty),
// and set (replace, or, and-not; on an empty stack it pushes).
func TestKeys_KittyStack(t *testing.T) {
	p, sink := newPane(t, 20, 2)
	steps := []struct{ seq, want string }{
		{"", "\x1b[?0u"},
		{"\x1b[>u", "\x1b[?0u"},
		{"\x1b[>1u", "\x1b[?1u"},
		{"\x1b[>3u", "\x1b[?3u"},
		{"\x1b[<u", "\x1b[?1u"},
		{"\x1b[>2u\x1b[>5u\x1b[<2u", "\x1b[?1u"},
		{"\x1b[<9u", "\x1b[?0u"},
		{"\x1b[=5u", "\x1b[?5u"},
		{"\x1b[=2;2u", "\x1b[?7u"},
		{"\x1b[=1;3u", "\x1b[?6u"},
		{"\x1b[=8;1u", "\x1b[?8u"},
		{"\x1b[=3u", "\x1b[?3u"},
	}
	for _, s := range steps {
		_, _ = p.Write([]byte(s.seq))
		assert.Equal(t, s.want, kittyFlagsOf(t, p, sink), "after %q", s.seq)
	}
	assert.Empty(t, p.Snapshot().Text()[0], "none of it reaches the screen")
}

// xterm modifyOtherKeys: CSI > 4 ; n m sets the level, CSI > 4 m resets it,
// and another resource is not this one.
func TestKeys_ModifyOtherKeysLevel(t *testing.T) {
	p, sink := newPane(t, 20, 2)
	ctrl1 := KeyPress{Code: '1', Mod: uv.ModCtrl}
	ctrlEnter := KeyPress{Code: uv.KeyEnter, Mod: uv.ModCtrl}
	send := func(seq string, k KeyPress) string {
		_, _ = p.Write([]byte(seq))
		p.SendKey(k)
		return sink.take()
	}
	assert.Equal(t, "\r", send("", ctrlEnter))
	assert.Equal(t, "\x1b[27;5;13~", send("\x1b[>4;1m", ctrlEnter), "level 1")
	assert.Equal(t, "", send("", ctrl1), "level 1 leaves ctrl+1 unsaid")
	assert.Equal(t, "\x1b[27;5;49~", send("\x1b[>4;2m", ctrl1), "level 2")
	assert.Equal(t, "\x1b[27;5;49~", send("\x1b[>1;0m", ctrl1), "another resource")
	assert.Equal(t, "\r", send("\x1b[>4m", ctrlEnter), "reset")
	assert.Empty(t, p.Snapshot().Text()[0])
}

// Scroll regions and left/right margins: a parameter past the screen is
// clamped and the rest of the sequence still applies; an empty region is
// ignored by the emulator, as xterm ignores it.
func TestMargins_ClampAndApply(t *testing.T) {
	rows := "\x1b[HA\r\nB\r\nC\r\nD\r\nE"
	for _, c := range []struct {
		name, seq string
		want      []string
	}{
		{"top only", rows + "\x1b[3r\x1b[3;1H\x1bM", []string{"A", "B", "", "C", "D"}},
		{"bottom past the screen", rows + "\x1b[2;9r\x1b[2;1H\x1bM", []string{"A", "", "B", "C", "D"}},
		{"an empty region leaves the last", rows + "\x1b[2;4r\x1b[4;2r\x1b[2;1H\x1bM", []string{"A", "", "B", "C", "E"}},
		{"left/right margins", "\x1b[Habcdef\x1b[?69h\x1b[2;4s\x1b[1;2H\x1b[@", []string{"a bcef", "", "", "", ""}},
		{"right margin past the screen", "\x1b[Habcdef\x1b[?69h\x1b[2;9s\x1b[1;2H\x1b[@", []string{"a bcde", "", "", "", ""}},
		{"left margin only", "\x1b[Habcdef\x1b[?69h\x1b[3s\x1b[1;4H\x1b[@", []string{"abc de", "", "", "", ""}},
	} {
		p, _ := newPane(t, 6, 5)
		require.NotPanics(t, func() { _, _ = p.Write([]byte(c.seq)) }, c.name)
		assert.Equal(t, c.want, p.Snapshot().Text(), c.name)
	}
}

func TestClose_ReturnsOnceTheDrainHasEnded(t *testing.T) {
	sink := newChildSink()
	p := NewXVT(10, 2, sink, Callbacks{})
	require.NoError(t, p.Close())
	select {
	case <-p.drained:
	default:
		t.Fatal("Close returned before the reply drain ended")
	}
}

func TestSnapshot_WideGraphemesLeaveZeroWidthColumns(t *testing.T) {
	p, _ := newPane(t, 6, 1)
	_, _ = p.Write([]byte("日a本"))
	row := p.Snapshot().Cells[0]
	assert.Equal(t, []int{2, 0, 1, 2, 0, 1}, []int{row[0].Width, row[1].Width, row[2].Width, row[3].Width, row[4].Width, row[5].Width})
	assert.Equal(t, []string{"日", "", "a", "本", "", " "}, []string{row[0].Content, row[1].Content, row[2].Content, row[3].Content, row[4].Content, row[5].Content})
}

func TestSendKey_UnencodableSendsNothing(t *testing.T) {
	p, sink := newPane(t, 10, 2)
	p.SendKey(KeyPress{Code: uv.KeyF13})
	p.SendKey(KeyPress{Code: 'a', Text: "a"})
	p.SendKey(KeyPress{Code: 'b', Text: "b"})
	assert.Equal(t, "ab", sink.take(), "nothing for F13; keys after it still go")
}
