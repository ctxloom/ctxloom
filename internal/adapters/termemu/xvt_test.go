package termemu

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// childSink collects what the pane sends to the child. Every write wakes
// waitFor, which re-checks instead of polling.
type childSink struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	wake chan struct{} // closed, and replaced, by every write
}

func newChildSink() *childSink { return &childSink{wake: make(chan struct{})} }

func (c *childSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.wake)
	c.wake = make(chan struct{})
	return c.buf.Write(p)
}

func (c *childSink) take() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.buf.String()
	c.buf.Reset()
	return s
}

// waitFor blocks until the sink has received want (replies cross a pipe and
// a goroutine on their way out), then takes everything it holds.
func (c *childSink) waitFor(t *testing.T, want string) string {
	t.Helper()
	expired := testsupport.Expiry(t)
	for {
		c.mu.Lock()
		got, wake := c.buf.String(), c.wake
		c.mu.Unlock()
		if strings.Contains(got, want) {
			return c.take()
		}
		select {
		case <-wake:
		case <-expired:
			t.Fatalf("child never received %q; got %q", want, got)
		}
	}
}

func newPane(t *testing.T, cols, rows int) (*XVT, *childSink) {
	t.Helper()
	return newPaneWith(t, cols, rows, Callbacks{})
}

func newPaneWith(t *testing.T, cols, rows int, cb Callbacks) (*XVT, *childSink) {
	t.Helper()
	sink := newChildSink()
	p := NewXVT(cols, rows, sink, cb)
	t.Cleanup(func() { _ = p.Close() })
	return p, sink
}

// x#974, the upstream repro verbatim: unpatched x/vt panics, the adapter
// does not. The first half pins that the bug is still upstream at the pinned
// version, so dropping the clamp is a decision made against a live panic.
func TestResize_MarginPastScreenPanicsUpstreamNotInAdapter(t *testing.T) {
	assert.Panics(t, func() {
		e := vt.NewEmulator(40, 27)
		go func() { _, _ = e.Read(make([]byte, 64)) }()
		_, _ = e.Write([]byte("\x1b[1;29r"))
		_, _ = e.Write([]byte("top\x1b[H\x1bM"))
	}, "x#974 no longer reproduces upstream: the adapter's DECSTBM clamp may be removable")

	p, _ := newPane(t, 40, 27)
	assert.NotPanics(t, func() {
		_, _ = p.Write([]byte("\x1b[1;29r"))
		_, _ = p.Write([]byte("top\x1b[H\x1bM"))
		_, _ = p.Write([]byte("\x1b[?69h\x1b[1;60s\x1b[H\x1bM"))
	})
}

// A stale DECSTBM after a shrink is CLAMPED (xterm), not dropped: the region
// still takes effect, ending at the new last row.
func TestResize_MarginIsClampedNotIgnored(t *testing.T) {
	p, _ := newPane(t, 10, 5)
	p.Resize(10, 4)
	// Region rows 2..(9→4); write a marker at the region top then reverse-index:
	// the region (rows 2-4) scrolls down, row 1 is untouched.
	_, _ = p.Write([]byte("\x1b[H" + "AAAA\r\nBBBB\r\nCCCC\r\nDDDD" + "\x1b[2;9r\x1b[2;1H\x1bM"))
	assert.Equal(t, []string{"AAAA", "", "BBBB", "CCCC"}, p.Snapshot().Text())
}

func TestSync_FrameCommitsOnlyAtBoundary(t *testing.T) {
	p, _ := newPane(t, 20, 3)
	_, _ = p.Write([]byte("old"))
	before := p.Frame()

	// Open a frame and half-draw it: the committed frame must not move.
	_, _ = p.Write([]byte("\x1b[?2026h\x1b[2J\x1b[Hne"))
	assert.Equal(t, before, p.Frame(), "a half-drawn synchronized frame was committed")
	assert.True(t, p.Syncing())

	// Close it and, in the SAME write, open and half-draw the next one: the
	// committed frame is the closed one, not the end-of-write screen.
	_, _ = p.Write([]byte("w\x1b[?2026l\x1b[?2026h\x1b[2J\x1b[Hhalf"))
	got := p.Frame()
	assert.Greater(t, got.Seq, before.Seq)
	assert.True(t, strings.HasPrefix(frameText(got), "new"), "committed %q", frameText(got))
	assert.Contains(t, p.Snapshot().Text()[0], "half", "the live screen is the half-drawn one")
}

func TestSync_TimeoutReleasesAStuckFrame(t *testing.T) {
	p, _ := newPane(t, 20, 3)
	_, _ = p.Write([]byte("\x1b[?2026hstuck"))
	assert.True(t, p.Syncing())
	p.mu.Lock()
	p.syncSince = time.Now().Add(-2 * SyncTimeout)
	p.mu.Unlock()
	_, _ = p.Write([]byte("!"))
	assert.False(t, p.Syncing())
	assert.Contains(t, frameText(p.Frame()), "stuck!")
}

func TestReplies_DrainedSoWriteNeverBlocks(t *testing.T) {
	p, sink := newPane(t, 20, 3)
	done := make(chan struct{})
	go func() {
		// x#939: undrained, the first DA1 query blocks Write forever.
		for range 200 {
			_, _ = p.Write([]byte("\x1b[c\x1b[6n\x1b[?2026$p"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on an undrained reply")
	}
	got := sink.waitFor(t, "$y")
	assert.Contains(t, got, "\x1b[?2026;0$y", "DECRQM 2026 must report 'not recognised' so claude does not self-detect sync")
}

func TestKeys_ReachTheChildInEachProtocol(t *testing.T) {
	cases := []struct {
		name  string
		setup string // what the child sent first
		key   KeyPress
		want  string
	}{
		{"enter", "", KeyPress{Code: uv.KeyEnter}, "\r"},
		{"shift+enter legacy", "", KeyPress{Code: uv.KeyEnter, Mod: uv.ModShift}, "\x1b\r"},
		{"shift+enter modifyOtherKeys", "\x1b[>4;2m", KeyPress{Code: uv.KeyEnter, Mod: uv.ModShift}, "\x1b[27;2;13~"},
		{"shift+enter kitty", "\x1b[>1u", KeyPress{Code: uv.KeyEnter, Mod: uv.ModShift}, "\x1b[13;2u"},
		{"shift+enter after kitty pop", "\x1b[>1u\x1b[<u", KeyPress{Code: uv.KeyEnter, Mod: uv.ModShift}, "\x1b\r"},
		{"ctrl+c legacy", "", KeyPress{Code: 'c', Mod: uv.ModCtrl}, "\x03"},
		{"ctrl+c kitty", "\x1b[>1u", KeyPress{Code: 'c', Mod: uv.ModCtrl}, "\x1b[99;5u"},
		{"esc legacy", "", KeyPress{Code: uv.KeyEscape}, "\x1b"},
		{"esc kitty", "\x1b[>1u", KeyPress{Code: uv.KeyEscape}, "\x1b[27u"},
		{"text", "", KeyPress{Code: 'a', Text: "a"}, "a"},
		{"shifted text", "", KeyPress{Code: 'a', Text: "A", Mod: uv.ModShift}, "A"},
		{"up normal", "", KeyPress{Code: uv.KeyUp}, "\x1b[A"},
		{"up app cursor", "\x1b[?1h", KeyPress{Code: uv.KeyUp}, "\x1bOA"},
		{"ctrl+up", "", KeyPress{Code: uv.KeyUp, Mod: uv.ModCtrl}, "\x1b[1;5A"},
		{"shift+tab", "", KeyPress{Code: uv.KeyTab, Mod: uv.ModShift}, "\x1b[Z"},
		{"alt+b", "", KeyPress{Code: 'b', Text: "b", Mod: uv.ModAlt}, "\x1bb"},
		{"pgup", "", KeyPress{Code: uv.KeyPgUp}, "\x1b[5~"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, sink := newPane(t, 20, 3)
			_, _ = p.Write([]byte(tc.setup))
			sink.take()
			p.SendKey(tc.key)
			assert.Equal(t, tc.want, sink.take())
		})
	}
}

func TestKeys_KittyQueryIsAnswered(t *testing.T) {
	p, sink := newPane(t, 20, 3)
	_, _ = p.Write([]byte("\x1b[?u"))
	assert.Equal(t, "\x1b[?0u", sink.waitFor(t, "u"))
	_, _ = p.Write([]byte("\x1b[>1u\x1b[?u"))
	assert.Equal(t, "\x1b[?1u", sink.waitFor(t, "u"))
}

func TestMouseAndPaste_ReachTheChild(t *testing.T) {
	p, sink := newPane(t, 80, 24)
	// Nothing until the child enables a mouse mode.
	p.SendMouse(uv.MouseClickEvent{X: 4, Y: 2, Button: uv.MouseLeft})
	assert.Empty(t, sink.take())

	// claude enables 1000 (or 1002/1003) and SGR 1006.
	_, _ = p.Write([]byte("\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?2004h"))
	p.SendMouse(uv.MouseClickEvent{X: 4, Y: 2, Button: uv.MouseLeft})
	assert.Equal(t, "\x1b[<0;5;3M", sink.waitFor(t, "M"))
	p.SendMouse(uv.MouseReleaseEvent{X: 4, Y: 2, Button: uv.MouseLeft})
	assert.Equal(t, "\x1b[<0;5;3m", sink.waitFor(t, "m"))
	p.SendMouse(uv.MouseWheelEvent{X: 10, Y: 5, Button: uv.MouseWheelUp})
	assert.Equal(t, "\x1b[<64;11;6M", sink.waitFor(t, "M"))
	p.SendMouse(uv.MouseWheelEvent{X: 10, Y: 5, Button: uv.MouseWheelDown})
	assert.Equal(t, "\x1b[<65;11;6M", sink.waitFor(t, "M"))

	p.Paste("line one\nline two")
	assert.Equal(t, "\x1b[200~line one\nline two\x1b[201~", sink.waitFor(t, "\x1b[201~"))
}

func TestWidths_EmojiAndCJK(t *testing.T) {
	cases := []struct {
		name, text string
		widths     []int // width of each grapheme cell in order
	}{
		{"cjk", "日本語", []int{2, 2, 2}},
		{"hangul", "한글", []int{2, 2}},
		{"emoji", "👍✅", []int{2, 2}},
		{"latin combining", "e\u0301x", []int{1, 1}},
		{"emoji + VS16", "\u2764\ufe0f!", []int{2, 1}},
		{"box drawing", "╭─╮", []int{1, 1, 1}},
	}
	for _, tc := range cases {
		for _, split := range []bool{false, true} {
			if split && strings.ContainsAny(tc.text, "\u0301\ufe0f") {
				continue // x#935: see TestWidths_ClusterSplitAcrossWrites
			}
			name := tc.name
			if split {
				name += " byte-at-a-time"
			}
			t.Run(name, func(t *testing.T) {
				p, _ := newPane(t, 20, 2)
				if split {
					for i := range len(tc.text) {
						_, _ = p.Write([]byte{tc.text[i]})
					}
				} else {
					_, _ = p.Write([]byte(tc.text))
				}
				s := p.Snapshot()
				var got []int
				for _, c := range s.Cells[0] {
					if c.Width > 0 && strings.TrimSpace(c.Content) != "" {
						got = append(got, c.Width)
					}
				}
				assert.Equal(t, tc.widths, got)
				assert.Equal(t, tc.text, s.Text()[0])
				sum := 0
				for _, w := range tc.widths {
					sum += w
				}
				assert.Equal(t, sum, s.CursorX, "cursor column after the text")
			})
		}
	}
}

// x#935 (open upstream): a grapheme cluster split across two Write calls
// is flushed at the end of the first, and the continuation (a combining
// mark, VS16, a ZWJ tail) starts a new cell or is dropped. A pty read can end
// anywhere, so this is reachable; the replay tests measure how often claude's
// stream actually splits there. Pinned so an upstream fix is noticed.
func TestWidths_ClusterSplitAcrossWrites(t *testing.T) {
	p, _ := newPane(t, 20, 2)
	_, _ = p.Write([]byte("e"))
	_, _ = p.Write([]byte("\u0301x"))
	assert.Equal(t, "ex", p.Snapshot().Text()[0], "x#935 fixed upstream? the combining mark now survives a split write")
}

// x#848: a Dingbat in an OSC title (claude's spinner, every tick) must not
// end the OSC and leak the rest of the title into the screen.
func TestOSC_DingbatTitleDoesNotLeak(t *testing.T) {
	for _, title := range []string{"\x1b]0;\u2733 Claude Code\x07", "\x1b]0;\u273b Busy\x1b\\", "\x1b]2;\u65e5\u672c\x07"} {
		p, _ := newPane(t, 40, 2)
		_, _ = p.Write([]byte("ab" + title + "cd"))
		assert.Equal(t, "abcd", p.Snapshot().Text()[0], "%q", title)
	}
}

// frameText is a frame's text, row by row, trailing blanks trimmed.
func frameText(f Frame) string {
	rows := make([]string, f.Buf.Height())
	for y := range rows {
		rows[y] = strings.TrimRight(stripANSI(f.Line(y)), " ")
	}
	return strings.Join(rows, "\n")
}

// stripANSI drops escape sequences from a rendered frame for assertions.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i < len(s) && s[i] == '[' {
			for i++; i < len(s) && (s[i] < 0x40 || s[i] > 0x7e); i++ {
			}
		}
	}
	return b.String()
}
