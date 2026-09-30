package termui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
)

// barRow is what the human sees on the bar row: the whole tty stream so far
// rendered onto a terminal model.
func barRow(h *ctlHarness) string {
	s := vtemu.New(24, 80)
	s.Feed([]byte(h.tty.String()))
	return s.Row(23)
}

// TestController_NoteBarShowsThenClearsOnItsClock: a note is on the bar for
// exactly its duration, measured on the controller's clock.
func TestController_NoteBarShowsThenClearsOnItsClock(t *testing.T) {
	clk := fakeclock.New()
	h := newCtlHarness(t, func(o *Options) { o.Clock = clk })
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	h.c.NoteBar("approval resolved (timed out)", 10*time.Second)
	assert.Contains(t, barRow(h), "approval resolved (timed out)")
	clk.Advance(10*time.Second - time.Millisecond)
	assert.Contains(t, barRow(h), "approval resolved (timed out)", "still inside its duration")
	clk.Advance(time.Millisecond)
	assert.NotContains(t, barRow(h), "approval resolved", "gone once its duration is up")
	assert.Contains(t, barRow(h), "perky-same-chevy", "the rest of the bar stays")
}

// TestController_ANewerNoteOutlivesTheOlderOnesTimer: one note at a time; a
// replaced note's timer never clears its replacement.
func TestController_ANewerNoteOutlivesTheOlderOnesTimer(t *testing.T) {
	clk := fakeclock.New()
	h := newCtlHarness(t, func(o *Options) { o.Clock = clk })
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	h.c.NoteBar("first", 5*time.Second)
	clk.Advance(3 * time.Second)
	h.c.NoteBar("second", 5*time.Second)
	assert.NotContains(t, barRow(h), "first")
	clk.Advance(2 * time.Second)
	assert.Contains(t, barRow(h), "second", "the first note's timer does not clear the second")
	clk.Advance(3 * time.Second)
	assert.NotContains(t, barRow(h), "second")
}

// lateTimerClock models a timer whose callback has already started when it is
// stopped: stop cannot cancel it, and the test runs the callbacks itself.
type lateTimerClock struct {
	*fakeclock.Clock
	fns []func()
}

func (c *lateTimerClock) AfterFunc(_ time.Duration, f func()) func() bool {
	c.fns = append(c.fns, f)
	return func() bool { return false }
}

// TestController_AReplacedNotesLateTimerDoesNotClearItsReplacement: the real
// race — the first note's timer fired and waited while a second note went up;
// it must not clear the second.
func TestController_AReplacedNotesLateTimerDoesNotClearItsReplacement(t *testing.T) {
	clk := &lateTimerClock{Clock: fakeclock.New()}
	h := newCtlHarness(t, func(o *Options) { o.Clock = clk })
	h.src <- &agent.WindowSize{Rows: 24, Cols: 80}
	_ = h.drainTranslated(t)
	waitFor(t, "surround establish", func() bool { return strings.Contains(h.tty.String(), "\x1b[1;23r") })

	h.c.NoteBar("first", time.Second)
	h.c.NoteBar("second", time.Second)
	fns := clk.fns
	fns[0]() // the first note's timer, late
	assert.Contains(t, barRow(h), "second")
	fns[1]()
	assert.NotContains(t, barRow(h), "second", "its own timer clears it")
}
