package termemu

import (
	"encoding/base64"
	"image/color"
	"strings"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A committed frame is a copy: the child drawing on afterwards changes the
// next frame, never one a renderer already holds.
func TestFrame_IsACopyTheChildCannotChange(t *testing.T) {
	p, _ := newPane(t, 10, 2)
	_, _ = p.Write([]byte("abc"))
	held := p.Frame()
	_, _ = p.Write([]byte("\x1b[Hxyz"))
	assert.Equal(t, "abc\n", frameText(held), "the held frame moved with the child")
	next := p.Frame()
	assert.Equal(t, "xyz\n", frameText(next))
	assert.Greater(t, next.Seq, held.Seq)
}

func TestFrame_LineIsTheRowAtFullWidthWithItsStyleClosed(t *testing.T) {
	p, _ := newPane(t, 8, 3)
	_, _ = p.Write([]byte("\x1b[31mred\x1b[0m ok\r\n日本\r\n\x1b[1mboldbold"))
	f := p.Frame()
	for y := range 3 {
		assert.Equal(t, 8, ansi.StringWidth(f.Line(y)), "row %d is the frame's width", y)
	}
	assert.Equal(t, "red ok  ", stripANSI(f.Line(0)))
	assert.True(t, strings.HasPrefix(f.Line(0), "\x1b[31m"), "the row keeps its colour: %q", f.Line(0))
	assert.Equal(t, "日本    ", stripANSI(f.Line(1)), "a wide grapheme is written once")
	assert.True(t, strings.HasSuffix(f.Line(2), "boldbold"+ansi.ResetStyle), "a row styled to its last column closes its style: %q", f.Line(2))
	assert.Empty(t, f.Line(-1))
	assert.Empty(t, f.Line(3))
	assert.Empty(t, Frame{}.Line(0), "a frame with no buffer has no rows")
}

func TestFrame_CursorIsTheCommittedOne(t *testing.T) {
	p, _ := newPane(t, 10, 3)
	_, _ = p.Write([]byte("\x1b[2;4H"))
	f := p.Frame()
	assert.Equal(t, uv.Pos(3, 1), f.Cursor)
	assert.True(t, f.CursorVisible)

	// Inside a synchronized frame the cursor moves on the live screen only.
	_, _ = p.Write([]byte("\x1b[?2026h\x1b[3;9H\x1b[?25l"))
	held := p.Frame()
	assert.Equal(t, uv.Pos(3, 1), held.Cursor, "the half-drawn frame's cursor leaked")
	assert.True(t, held.CursorVisible)
	_, _ = p.Write([]byte("\x1b[?2026l"))
	f = p.Frame()
	assert.Equal(t, uv.Pos(8, 2), f.Cursor)
	assert.False(t, f.CursorVisible, "the child hid its cursor")
}

// recorder collects callbacks; they run on the writer's goroutine.
type recorder struct {
	mu       sync.Mutex
	titles   []string
	bells    int
	clips    []string
	tracking []MouseTracking
}

func (r *recorder) callbacks() Callbacks {
	return Callbacks{
		Title: func(s string) { r.mu.Lock(); r.titles = append(r.titles, s); r.mu.Unlock() },
		Bell:  func() { r.mu.Lock(); r.bells++; r.mu.Unlock() },
		Clipboard: func(sel byte, data string) {
			r.mu.Lock()
			r.clips = append(r.clips, string(sel)+":"+data)
			r.mu.Unlock()
		},
		Mouse: func(m MouseTracking) { r.mu.Lock(); r.tracking = append(r.tracking, m); r.mu.Unlock() },
	}
}

func TestCallbacks_TitleAndBell(t *testing.T) {
	r := &recorder{}
	p, _ := newPaneWith(t, 20, 2, r.callbacks())
	_, _ = p.Write([]byte("\x1b]0;✳ Claude\x07\x1b]2;two\x1b\\\x07x\x07"))
	assert.Equal(t, []string{"✳ Claude", "two"}, r.titles)
	assert.Equal(t, 2, r.bells)
	assert.Equal(t, "x", p.Snapshot().Text()[0], "neither reaches the screen")
}

func TestCallbacks_ClipboardWritesOnlyDecoded(t *testing.T) {
	r := &recorder{}
	p, sink := newPaneWith(t, 20, 2, r.callbacks())
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	_, _ = p.Write([]byte("\x1b]52;c;" + b64("hello") + "\x07"))
	_, _ = p.Write([]byte("\x1b]52;;" + b64("default") + "\x1b\\"))
	_, _ = p.Write([]byte("\x1b]52;p;" + b64("primary") + "\x07"))
	_, _ = p.Write([]byte("\x1b]52;c;?\x07"))
	_, _ = p.Write([]byte("\x1b]52;c;not*base64\x07"))
	_, _ = p.Write([]byte("\x1b]52;c\x07"))
	assert.Equal(t, []string{"c:hello", "c:default", "p:primary"}, r.clips)

	// Replies are ordered through one pipe: once the DA1 answer is in, any
	// answer to the read would be in front of it.
	_, _ = p.Write([]byte("\x1b[c"))
	got := sink.waitFor(t, "c")
	assert.NotContains(t, got, "52", "a clipboard read was answered: %q", got)
	assert.Equal(t, "", p.Snapshot().Text()[0], "no OSC 52 leaks onto the screen")
}

func TestCallbacks_ClipboardWithNoCallbackIsDropped(t *testing.T) {
	p, _ := newPane(t, 20, 2)
	_, _ = p.Write([]byte("\x1b]52;c;aGk=\x07ok"))
	assert.Equal(t, "ok", p.Snapshot().Text()[0])
}

func TestCallbacks_MouseTrackingFollowsTheHighestMode(t *testing.T) {
	r := &recorder{}
	p, _ := newPaneWith(t, 20, 2, r.callbacks())
	for _, seq := range []string{
		"\x1b[?1000h", "\x1b[?1000h", // a repeat is no change
		"\x1b[?1006h", // SGR encoding is not tracking
		"\x1b[?1002h", "\x1b[?1003h",
		"\x1b[?1003l", "\x1b[?1002l", "\x1b[?1000l",
		"\x1b[?9h", "\x1b[?9l",
		"\x1b[?1003h", "\x1b[?1000h", "\x1b[?1003l",
	} {
		_, _ = p.Write([]byte(seq))
	}
	assert.Equal(t, []MouseTracking{
		MouseClicks, MouseDrags, MouseMotion, MouseDrags, MouseClicks, MouseOff,
		MouseClicks, MouseOff,
		MouseMotion, MouseClicks,
	}, r.tracking)
}

// The child asks its terminal's colours to choose a theme; the answer must be
// the human's terminal's, not x/vt's white-on-black.
func TestSetDefaultColors_AnswerTheChildsColourQueries(t *testing.T) {
	p, sink := newPane(t, 20, 2)
	fg := color.RGBA{R: 0x10, G: 0x20, B: 0x30, A: 0xff}
	bg := color.RGBA{R: 0xfa, G: 0xf0, B: 0xe0, A: 0xff}
	p.SetDefaultColors(fg, bg)
	_, _ = p.Write([]byte("\x1b]11;?\x07"))
	assert.Equal(t, ansi.SetBackgroundColor(ansi.XRGBColor{Color: bg}.String()), sink.waitFor(t, "\a"))
	_, _ = p.Write([]byte("\x1b]10;?\x07"))
	assert.Equal(t, ansi.SetForegroundColor(ansi.XRGBColor{Color: fg}.String()), sink.waitFor(t, "\a"))

	other := color.RGBA{R: 1, G: 2, B: 3, A: 0xff}
	p.SetDefaultColors(nil, other)
	_, _ = p.Write([]byte("\x1b]10;?\x07"))
	assert.Equal(t, ansi.SetForegroundColor(ansi.XRGBColor{Color: fg}.String()), sink.waitFor(t, "\a"), "a nil colour leaves that one alone")
	p.SetDefaultColors(other, nil)
	_, _ = p.Write([]byte("\x1b]11;?\x07"))
	assert.Equal(t, ansi.SetBackgroundColor(ansi.XRGBColor{Color: other}.String()), sink.waitFor(t, "\a"))
}

func TestFocus_ReportsOnlyWhenTheChildAsked(t *testing.T) {
	p, sink := newPane(t, 20, 2)
	p.Focus(true)
	p.Focus(false)
	_, _ = p.Write([]byte("\x1b[c"))
	require.NotContains(t, sink.waitFor(t, "c"), "\x1b[I", "a child that never asked was told")

	_, _ = p.Write([]byte("\x1b[?1004h"))
	p.Focus(true)
	assert.Equal(t, "\x1b[I", sink.waitFor(t, "\x1b[I"))
	p.Focus(false)
	assert.Equal(t, "\x1b[O", sink.waitFor(t, "\x1b[O"))
}
