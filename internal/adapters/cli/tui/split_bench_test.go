package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// benchFrame is one full redraw of a 120x40 engine screen in the shape a
// streaming claude turn draws: styled diff lines, wide graphemes, box rules.
func benchFrame(n int) string {
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[H")
	for y := range 40 {
		switch y % 4 {
		case 0:
			fmt.Fprintf(&b, "\x1b[32m+ added line %d of the diff 日本語 ✅\x1b[0m\x1b[K\r\n", n+y)
		case 1:
			fmt.Fprintf(&b, "\x1b[31m- removed line %d\x1b[0m with \x1b[1mbold\x1b[0m text\x1b[K\r\n", n+y)
		case 2:
			fmt.Fprintf(&b, "\x1b[2m│\x1b[0m context %d %s\x1b[K\r\n", y, strings.Repeat("─", 60))
		default:
			fmt.Fprintf(&b, "  plain text line %d\x1b[K\r\n", y)
		}
	}
	b.WriteString("\x1b[?2026l")
	return b.String()
}

func benchSplit(b *testing.B) *splitHarness {
	port := newSplitPort()
	for i := range 6 {
		port.child(fmt.Sprintf("child-%d", i), "coder", selfHarp, coord.StateExecuting)
	}
	return newSplitHarness(b, port).sized(rosterPaneWidth+1+120, 40)
}

// BenchmarkSplit_FrameToView is one engine frame end to end on the
// compositor's side: the emulator parses the redraw, the frame is adopted
// (each row serialized once) and the view is composed.
func BenchmarkSplit_FrameToView(b *testing.B) {
	h := benchSplit(b)
	frames := []string{benchFrame(0), benchFrame(1)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = h.pane.Write([]byte(frames[i%2]))
		h.s.adoptFrame()
		_ = h.s.View()
	}
}

// BenchmarkSplit_ViewOnly is a view with no new frame: what every other
// message (a key, a roster read) costs to redraw.
func BenchmarkSplit_ViewOnly(b *testing.B) {
	h := benchSplit(b)
	_, _ = h.pane.Write([]byte(benchFrame(0)))
	h.s.adoptFrame()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = h.s.View()
	}
}
