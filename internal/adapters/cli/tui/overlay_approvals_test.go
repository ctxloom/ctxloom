package tui

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// lockedScreen is a terminal model the program's renderer writes to from its
// own goroutine while the test reads it.
type lockedScreen struct {
	mu sync.Mutex
	s  *vtemu.Screen
}

func (l *lockedScreen) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.s.Write(p)
}

func (l *lockedScreen) read(f func(s *vtemu.Screen)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(l.s)
}

func (l *lockedScreen) text() (out string) {
	l.read(func(s *vtemu.Screen) { out = s.String() })
	return out
}

func (l *lockedScreen) row(i int) (out string) {
	l.read(func(s *vtemu.Screen) { out = s.Row(i) })
	return out
}

// hostileCommand tries everything a child could put in a Bash command to
// repaint the modal: clear and home, a fake button, an OSC title, a bidi
// override.
const hostileCommand = "rm -rf build\x1b[2J\x1b[H[ Allow ]\x1b]0;pwned\x07 \u202egnp.exe"

// TestOverlay_SummonedModalFillsTheScreenInertlyAndAnswers renders the
// summoned modal through a real program onto a terminal model and judges
// what the human sees: the frame on every drawable row (nothing scrolled,
// no alternate screen of its own — termui took the screen already), the
// child's escapes shown as text rather than obeyed, and navigate + Enter,
// once armed, answering the request it was aimed at — after which the modal
// closes itself.
func TestOverlay_SummonedModalFillsTheScreenInertlyAndAnswers(t *testing.T) {
	q := newFakeQueue(bashReq("a", "wiry-otter", hostileCommand, 10*time.Minute))
	o := NewOverlay(context.Background(), Sources{Approvals: q}, 0x1d, termui.OverlayStart{Summoned: true, View: "approvals"})
	scr := &lockedScreen{s: vtemu.New(30, 100)}
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	done := make(chan error, 1)
	go func() { done <- o.Run(pr, scr, termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}) }()

	waitForOverlay(t, "the modal's bottom border on the last row", func() bool { return strings.HasPrefix(scr.row(29), "└") })
	assert.True(t, strings.HasPrefix(scr.row(0), "┌ ctxloom approval ─ keys here never reach the engine"), "row 0: %q", scr.row(0))
	scr.read(func(s *vtemu.Screen) {
		assert.Empty(t, s.Scrollback(), "a full-height frame scrolled the screen")
		assert.False(t, s.OnAltScreen(), "the modal entered an alternate screen of its own")
	})
	screen := scr.text()
	assert.Contains(t, screen, "$ rm -rf build⟨ESC⟩[2J⟨ESC⟩[H[ Allow ]⟨ESC⟩]0;pwned⟨U+0007⟩ ⟨U+202E⟩gnp.exe",
		"the child's escapes are on screen as text — so none of them acted")
	assert.Contains(t, screen, "arming — keys are ignored for a moment")

	o.Armed(0)
	waitForOverlay(t, "armed", func() bool { return !strings.Contains(scr.text(), "arming —") })
	_, err := pw.Write([]byte("\x1b[C"))
	require.NoError(t, err)
	waitForOverlay(t, "focus on Allow once", func() bool { return strings.Contains(scr.text(), "[ Allow once ]") })
	_, err = pw.Write([]byte("\r"))
	require.NoError(t, err)
	waitForOverlay(t, "the answer", func() bool { return len(q.calls()) == 1 })
	assert.Equal(t, answerCall{id: "a", d: coord.ApprovalDecision{Allow: true}}, q.calls()[0])
	select {
	case err := <-done:
		require.NoError(t, err, "the modal closed itself once nothing was pending")
	case <-time.After(20 * time.Second):
		t.Fatal("the modal stayed up with nothing pending")
	}
}

// TestOverlay_NotifyBeforeRunStillShowsTheBanner: a notice that arrives
// before the program exists is kept and shown, naming the asking harp.
func TestOverlay_NotifyBeforeRunStillShowsTheBanner(t *testing.T) {
	q := newFakeQueue(bashReq("a", "wiry-otter", "ls", 10*time.Minute))
	f := newFakeSources(t.TempDir(), RosterRow{Harp: "self"})
	src := f.sources()
	src.Approvals = q
	o := NewOverlay(context.Background(), src, 0x1d, termui.OverlayStart{})
	o.Notify(termui.Notice{Text: "approval from wiry-otter"})
	scr := &lockedScreen{s: vtemu.New(30, 100)}
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	done := make(chan error, 1)
	go func() { done <- o.Run(pr, scr, termui.OverlayGeometry{Cols: 100, Rows: 30, PanelRows: 10}) }()
	waitForOverlay(t, "the banner", func() bool {
		return strings.Contains(scr.text(), "⚑ approval from wiry-otter — a to review")
	})
	o.Abort()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the overlay never exited")
	}
}
