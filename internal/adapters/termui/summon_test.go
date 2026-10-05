package termui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// summonHarness is a sized controller on the fake clock with the real stdin
// pump.
type summonHarness struct{ *ctlHarness }

func newSummonHarness(t *testing.T, mutate func(*Options)) *summonHarness {
	t.Helper()
	h := newCtlHarness(t, mutate)
	h.sized(t, 24, 80)
	return &summonHarness{ctlHarness: h}
}

// typeToEngine types keys bound for the engine: the read that carried them
// has been scanned, so the quiet gate has seen it.
func (h *summonHarness) typeToEngine(t *testing.T, s string) {
	t.Helper()
	want := h.engine.String() + s
	h.typeKeys(t, s)
	require.Equal(t, want, h.engine.String(), "the engine received %q", s)
}

// testNotice is what the tests' Summon asks an engaged overlay to show.
var testNotice = Notice{Text: "approval from wiry-otter"}

// summon runs Summon on its own goroutine; the result arrives on the channel.
func (h *summonHarness) summon(ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- h.c.Summon(ctx, OverlayStart{View: "approvals"}, testNotice) }()
	return done
}

// waitTimers waits until exactly n clock timers are armed: Summon, on its own
// goroutine, has made its attempt and is waiting on the clock.
func (h *summonHarness) waitTimers(t *testing.T, n int) {
	t.Helper()
	h.clk.waitPending(t, n)
}

func (h *summonHarness) discarded() int {
	h.c.ic.mu.Lock()
	defer h.c.ic.mu.Unlock()
	return h.c.ic.modal.discarded
}

func recvErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	return await(t, "Summon's return", ch)
}

func notStarted(t *testing.T, ov *fakeOverlay, why string) {
	t.Helper()
	select {
	case <-ov.started:
		t.Fatal(why)
	default:
	}
}

// TestSummon_QuietGate is T2: a human typing every second never loses the
// terminal to the modal; it appears QuietFor after the last key.
func TestSummon_QuietGate(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeToEngine(t, "a")
	done := h.summon(context.Background())
	h.waitTimers(t, 1)

	for _, k := range []string{"b", "c", "d"} {
		h.clk.Advance(time.Second)
		h.typeToEngine(t, k)
		h.waitTimers(t, 1)
		notStarted(t, h.overlay, "the modal took focus while the human was typing")
	}
	h.clk.Advance(1400 * time.Millisecond)
	h.waitTimers(t, 1)
	notStarted(t, h.overlay, "the modal took focus before 1.5s of quiet")

	h.clk.Advance(100 * time.Millisecond)
	require.NoError(t, recvErr(t, done))
	<-h.overlay.started
	assert.Equal(t, "abcd", h.engine.String(), "every key typed before the modal went to the engine")
}

// TestSummon_WaitsOutAnUnfinishedSequenceAndAPaste is lock 2: quiet is not
// enough while a sequence or a bracketed paste is still open.
func TestSummon_WaitsOutAnUnfinishedSequenceAndAPaste(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeToEngine(t, "\x1b[200~half a paste")
	done := h.summon(context.Background())
	h.waitTimers(t, 1)
	h.clk.Advance(5 * time.Second)
	h.waitTimers(t, 1)
	notStarted(t, h.overlay, "focus moved inside a bracketed paste")

	h.typeToEngine(t, " end\x1b[201~")
	h.clk.Advance(defaultQuietFor)
	require.NoError(t, recvErr(t, done))
	<-h.overlay.started
}

// TestSummon_ArmingDiscardsCountsAndRestarts is T3.
func TestSummon_ArmingDiscardsCountsAndRestarts(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.overlay.frame = "FRAME"
	require.NoError(t, recvErr(t, h.summon(context.Background())))
	<-h.overlay.started
	h.waitTimers(t, 1) // the arming window, started by the first frame

	h.typeKeys(t, "y1\r")
	require.Equal(t, 3, h.discarded(), "three keys discarded")
	h.clk.Advance(500 * time.Millisecond)
	h.typeKeys(t, "n")
	require.Equal(t, 4, h.discarded(), "a fourth")

	h.clk.Advance(250 * time.Millisecond) // the first window's end: moved by the key at 500ms
	h.waitTimers(t, 1)
	select {
	case n := <-h.overlay.armed:
		t.Fatalf("armed at %d discarded while a key had restarted the window", n)
	default:
	}
	h.clk.Advance(500 * time.Millisecond) // 750ms after the last key: Armed runs inside Advance
	select {
	case n := <-h.overlay.armed:
		assert.Equal(t, 4, n, "Armed reports every key the window swallowed")
	default:
		t.Fatal("never armed")
	}
	assert.Empty(t, h.overlay.ui.String(), "no key typed while arming reached the modal")
	assert.Empty(t, h.engine.String(), "nor the engine")

	h.typeKeys(t, "j")
	h.overlay.ui.waitUntil(t, "armed keys at the modal", func(s string) bool { return s == "j" })
	assert.Equal(t, 0, h.clk.Pending(), "Armed is called once; nothing re-arms")
}

// TestSummon_RepliesGoToTheEngine is T4 at the controller: under the modal a
// terminal reply still reaches the engine that asked, mouse reports reach
// nobody, keys reach the modal.
func TestSummon_RepliesGoToTheEngine(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.overlay.frame = "FRAME"
	require.NoError(t, recvErr(t, h.summon(context.Background())))
	<-h.overlay.started
	h.clk.Advance(defaultArmFor)
	<-h.overlay.armed

	h.typeKeys(t, "k\x1b[12;40R\x1b[<0;3;4M\x1b]11;rgb:0/0/0\x1b\\\x1b[Ij")
	assert.Equal(t, "\x1b[12;40R\x1b]11;rgb:0/0/0\x1b\\\x1b[I", h.engine.String(), "replies at the engine")
	h.overlay.ui.waitUntil(t, "keys at the modal", func(s string) bool { return s == "kj" })
}

// TestSummon_MainScreenReplaysByteExact is T5: over an engine on the main
// screen the modal takes the alternate screen, and dismissal comes back to
// the engine's screen with its held output replayed, in one write, then a
// nudge.
func TestSummon_MainScreenReplaysByteExact(t *testing.T) {
	h := newSummonHarness(t, nil)
	require.NoError(t, recvErr(t, h.summon(context.Background())))
	<-h.overlay.started
	assert.Contains(t, h.tty.String(), "\x1b[?1049h\x1b[r")

	_, _ = h.c.Stdout().Write([]byte("HELD-WHILE-MODAL"))
	assert.NotContains(t, h.tty.String(), "HELD-WHILE-MODAL")
	h.overlay.release <- nil
	assert.Equal(t, uint16(22), h.drainTranslated(t).Rows, "the nudge follows")
	out := h.tty.String()
	require.Contains(t, out, "HELD-WHILE-MODAL", "replayed before the nudge")
	assert.Less(t, strings.LastIndex(out, "\x1b[?1049l"), strings.Index(out, "HELD-WHILE-MODAL"))
	assert.Less(t, strings.LastIndex(out, "\x1b[1;23r"), strings.Index(out, "HELD-WHILE-MODAL"))
}

// TestSummon_AltScreenEngineIsRedrawn is T6: over an engine on the alternate
// screen the full-screen modal draws over it in place; nothing of its screen
// survives, so the held bytes are dropped, the screen cleared, and the
// nudge repaints.
func TestSummon_AltScreenEngineIsRedrawn(t *testing.T) {
	h := newSummonHarness(t, nil)
	_, _ = h.c.Stdout().Write([]byte("\x1b[?1049hfullscreen"))
	require.NoError(t, recvErr(t, h.summon(context.Background())))
	geo := <-h.overlay.started
	assert.True(t, geo.EngineOnAltScreen)
	before := h.tty.String()
	assert.NotContains(t, before[strings.Index(before, "fullscreen"):], "\x1b[?1049", "no alternate-screen switch of our own")
	assert.True(t, strings.HasSuffix(before, "\x1b7\x1b[r\x1b[H\x1b[2J"), "the modal starts on a cleared screen")

	_, _ = h.c.Stdout().Write([]byte("HELD-WHILE-MODAL"))
	h.overlay.release <- nil
	assert.Equal(t, uint16(22), h.drainTranslated(t).Rows, "the nudge follows")
	after := h.tty.String()[len(before):]
	assert.NotContains(t, after, "HELD-WHILE-MODAL", "never replayed onto a screen it was not written for")
	assert.True(t, strings.HasPrefix(after, "\x1b[H\x1b[2J"), "the screen is cleared for the repaint: %q", after)
	assert.Contains(t, after, "\x1b[1;23r", "and the region and bar come back")
}

// TestSummon_OverflowRedrawsWithANotice is T7 through the controller: a
// hold that overflowed is never replayed, whatever the screen.
func TestSummon_OverflowRedrawsWithANotice(t *testing.T) {
	h := newSummonHarness(t, func(o *Options) { o.ModalHoldCapacity = 16 })
	require.NoError(t, recvErr(t, h.summon(context.Background())))
	<-h.overlay.started
	before := len(h.tty.String())
	_, _ = h.c.Stdout().Write([]byte("0123456789\x1b[31"))
	_, _ = h.c.Stdout().Write([]byte("mTAIL"))
	h.overlay.release <- nil
	_ = h.drainTranslated(t)
	after := h.tty.String()[before:]
	assert.NotContains(t, after, "TAIL")
	assert.NotContains(t, after, "31m")
	assert.Contains(t, after, overflowText)
	assert.True(t, strings.HasPrefix(after, "\x1b[?1049l"), "back to the engine's screen first: %q", after)
}

// TestSummon_NeverTakesTheScreenFromAnOverlay is T8's first case.
func TestSummon_NeverTakesTheScreenFromAnOverlay(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	<-h.overlay.started
	err := recvErr(t, h.summon(context.Background()))
	require.ErrorIs(t, err, ErrOverlayEngaged)
	select {
	case n := <-h.overlay.notices:
		assert.Equal(t, testNotice, n, "the engaged overlay is told what the caller said — the asking harp")
	default:
		t.Fatal("the engaged overlay was not told")
	}
	h.overlay.ui.waitUntil(t, "the key", func(s string) bool { return s == "j" })
}

func TestSummon_UnavailableAndCancelled(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeToEngine(t, "a")

	ctx, cancel := context.WithCancel(context.Background())
	done := h.summon(ctx)
	h.waitTimers(t, 1)
	cancel()
	require.ErrorIs(t, recvErr(t, done), context.Canceled)

	done = h.summon(context.Background())
	h.waitTimers(t, 1)
	h.c.Close()
	require.ErrorIs(t, recvErr(t, done), ErrUIUnavailable, "Close during the wait ends it")
	h.clk.Advance(time.Hour) // every other gate open: only the closed layer refuses
	require.ErrorIs(t, recvErr(t, h.summon(context.Background())), ErrUIUnavailable)
	notStarted(t, h.overlay, "a closed layer showed an overlay")
}

func TestSummon_DegradedIsUnavailable(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	<-h.overlay.started
	h.overlay.release <- errors.New("viewer broke")
	await(t, "the degradation warning", h.warns) // degrade warns after it turns the layer off
	require.ErrorIs(t, recvErr(t, h.summon(context.Background())), ErrUIUnavailable)
}

// TestSummon_WaitsForATeardownThenTakesTheScreen pins that a Summon arriving
// while an overlay is closing waits for the release instead of failing.
func TestSummon_WaitsForATeardownThenTakesTheScreen(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.c.session.Lock() // a teardown in flight
	shown, retry, err := h.c.trySummon(OverlayStart{Summoned: true})
	assert.NoError(t, err, "a teardown in flight is a wait, not a failure")
	assert.False(t, shown)
	assert.Zero(t, retry, "woken by the teardown's end, not a timer")

	done := h.summon(context.Background())
	notStarted(t, h.overlay, "took the screen during a teardown")
	h.c.session.Unlock()
	h.c.changed.fire()
	require.NoError(t, recvErr(t, done))
	<-h.overlay.started
}

// TestController_ResizeReachesAnEngagedOverlay pins that a running overlay is
// relaid out on a terminal resize, with the same geometry rules as Run.
func TestController_ResizeReachesAnEngagedOverlay(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	<-h.overlay.started
	h.src <- &agent.WindowSize{Rows: 30, Cols: 100}
	geo := await(t, "the overlay's resize", h.overlay.resizes)
	assert.Equal(t, OverlayGeometry{Cols: 100, Rows: 29, PanelRows: 9}, geo)
}

// TestController_PasteStraddlingDismissalNeverReachesTheEngine pins that a
// paste the viewer began is not finished in the engine, unbracketed — where
// a newline inside it would submit whatever claude had typed.
func TestController_PasteStraddlingDismissalNeverReachesTheEngine(t *testing.T) {
	h := newSummonHarness(t, nil)
	h.typeKeys(t, string([]byte{testPrefix, 'j'}))
	<-h.overlay.started
	h.typeKeys(t, "\x1b[200~first half")
	h.overlay.ui.waitUntil(t, "the paste starting in the viewer", func(s string) bool { return strings.Contains(s, "first half") })
	h.overlay.release <- nil
	_ = h.drainTranslated(t) // the release nudge: disengaged
	h.typeKeys(t, "\rsecond half\x1b[201~")
	h.typeToEngine(t, "z")
	assert.Equal(t, "z", h.engine.String(), "the rest of the paste was drained; the key after it is the engine's")
}
