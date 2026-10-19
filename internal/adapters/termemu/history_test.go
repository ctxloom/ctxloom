package termemu

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// What scrolls off the top of the main screen is the pane's history: read
// back oldest first, styled as the frame's rows are, and counted on the
// frame it stood above when that frame was committed.
func TestHistory_TheLinesScrolledOffTheTop(t *testing.T) {
	p, _ := newPane(t, 10, 2)
	assert.Zero(t, p.Frame().History)
	assert.Empty(t, p.History(0, 5))

	_, _ = p.Write([]byte("one\r\n\x1b[1mtwo\x1b[m\r\nthree\r\nfour"))
	f := p.Frame()
	assert.Equal(t, 2, f.History, "two lines above the frame")
	assert.Equal(t, "three\nfour", frameText(f))
	assert.Equal(t, []string{"one", "\x1b[1mtwo" + ansi.ResetStyle}, p.History(0, 5), "oldest first, styled, only as wide as its content")
	assert.Equal(t, []string{"\x1b[1mtwo" + ansi.ResetStyle}, p.History(1, 1))
	assert.Empty(t, p.History(2, 1), "past the end")
	assert.Empty(t, p.History(-1, 0))
	assert.Equal(t, []string{"one"}, p.History(-3, 4), "a start before the oldest line reads from it")
}

// While a synchronized frame is open the committed frame keeps the history
// it was committed over, however much the half-drawn frame scrolls away.
func TestHistory_TheFrameKeepsItsCommittedCount(t *testing.T) {
	p, _ := newPane(t, 10, 2)
	_, _ = p.Write([]byte("one\r\ntwo\r\nthree"))
	assert.Equal(t, 1, p.Frame().History)
	_, _ = p.Write([]byte("\x1b[?2026h\r\nfour\r\nfive"))
	assert.Equal(t, 1, p.Frame().History, "the frame is the one before the sync opened")
	assert.Len(t, p.History(0, 9), 3, "the history itself is live")
	_, _ = p.Write([]byte("\x1b[?2026l"))
	assert.Equal(t, 3, p.Frame().History)
}
