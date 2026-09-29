package termui

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport/vtemu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the scroll-tear regression suite (fix/surround-scroll-tear):
// a child that resets the terminal's scroll margins (`\x1b[r`, RIS) used to
// silently unprotect the surround's reserved bottom row — periodic bar
// repaints then rode the child's scrolling up into the scrollback as torn
// copies — and bar repaints flushed at raw pty-chunk boundaries could land
// inside a split escape sequence or UTF-8 rune. The tests drive the REAL
// controller wiring (interceptor + gate + surround) with a scripted child
// stream, then replay the captured tty bytes through a VT screen model
// (testsupport/vtemu) and assert the invariants a live terminal needs:
//
//   - bar content only ever renders on the reserved bottom row (never above
//     it, never in scrollback);
//   - child escape sequences and runes arrive contiguously (no bar bytes
//     spliced into their middle);
//   - every bar repaint begins at a parser-ground boundary.

// ---------------------------------------------------------------------------
// Harness: the real controller wiring over a scripted child, with the clock
// frozen so "engine busy" is deterministic and roster repaints ride the
// gate's chunk flush exactly as in production.
// ---------------------------------------------------------------------------

// barMarker is a bar-only token: PrefixHint "^]" renders "^] viewer" at the
// bar's tail, so "viewer" appearing anywhere but the reserved row is a leak.
const barMarker = "viewer"

type tearHarness struct {
	t     *testing.T
	c     *Controller
	tty   *lockedBuffer
	emu   *vtemu.Screen
	fed   int
	now   *int64
	clean func()
}

func newTearHarness(t *testing.T, rows, cols int) *tearHarness {
	t.Helper()
	now := int64(1_000_000_000_000)
	restoreNow := nowNanos
	nowNanos = func() int64 { return now }

	pr, pw := io.Pipe()
	tty := &lockedBuffer{}
	src := make(chan *agent.WindowSize, 1)
	c := New(Options{
		Stdin:    pr,
		TTY:      tty,
		Resize:   src,
		Prefix:   testPrefix,
		Surround: true,
		Bar: BarInfo{
			Harp:       "minty-calm-diary",
			Agent:      "coordinator",
			Engine:     "claude-code",
			Model:      "claude-opus-4-8",
			PrefixHint: "^]",
		},
		NewOverlay: func() Overlay { return newFakeOverlay() },
	})
	src <- &agent.WindowSize{Rows: uint16(rows), Cols: uint16(cols)}
	waitFor(t, "surround establish", func() bool {
		return strings.Contains(tty.String(), fmt.Sprintf("\x1b[1;%dr", rows-1))
	})
	h := &tearHarness{t: t, c: c, tty: tty, emu: vtemu.New(rows, cols), now: &now}
	h.clean = func() {
		// Close first: it joins the goroutines that read nowNanos, so the
		// seam is restored only once nothing can still be reading it.
		c.Close()
		_ = pw.Close()
		close(src)
		nowNanos = restoreNow
	}
	t.Cleanup(h.clean)
	h.feed()
	return h
}

// child writes one engine chunk; the frozen clock marks the engine busy so a
// pending roster repaint flushes at this chunk's boundary (production path).
func (h *tearHarness) child(s string) {
	_, err := h.c.Stdout().Write([]byte(s))
	require.NoError(h.t, err)
}

// roster requests a bar repaint; with the clock frozen right after a child
// write it always defers to the gate's next afterWrite flush.
func (h *tearHarness) roster(state string) {
	h.c.sur.SetRoster([]RosterEntry{{Harp: "sixth-royal-kelp", State: state, LastActivityUnix: 1}})
}

// idle advances the frozen clock past the busy window so the next repaint
// request paints immediately (the RequestPaint fast path).
func (h *tearHarness) idle() { *h.now += int64(time.Second) }

// feed replays the tty bytes written since the last call into the emulator.
func (h *tearHarness) feed() {
	snap := h.tty.String()
	h.emu.Feed([]byte(snap[h.fed:]))
	h.fed = len(snap)
}

// assertNoBleed feeds pending bytes and asserts bar content sits ONLY on the
// reserved bottom row — never above it, never in the scrollback.
func (h *tearHarness) assertNoBleed() {
	h.t.Helper()
	h.feed()
	for i := 0; i < h.emu.Rows()-1; i++ {
		if strings.Contains(h.emu.Row(i), barMarker) {
			h.t.Fatalf("bar content leaked into the scrolling region: row %d = %q", i+1, h.emu.Row(i))
		}
	}
	for _, l := range h.emu.Scrollback() {
		if strings.Contains(l, barMarker) {
			h.t.Fatalf("bar content rode into the scrollback: %q", l)
		}
	}
}

// ---------------------------------------------------------------------------
// The live-incident regressions.
// ---------------------------------------------------------------------------

// TestSurround_ChildRegionResetCannotScrollBarAway is the live incident: the
// child resets the scroll margins (claude redrawing its bottom UI), engine
// output keeps scrolling, and periodic bar repaints fire between chunks. The
// reserved row must stay protected: no torn bar copies above the separator
// row or in scrollback.
func TestSurround_ChildRegionResetCannotScrollBarAway(t *testing.T) {
	h := newTearHarness(t, 24, 120)

	// The child believes the screen is 23 rows; `\x1b[r` is its "full screen"
	// margin reset. Unclamped, it exposes the real 24th row — the bar.
	h.child("\x1b[r")
	h.assertNoBleed()

	for i := 0; i < 40; i++ {
		h.roster("executing") // busy → dirty → flush at next chunk boundary
		h.child(fmt.Sprintf("chld transcript line %02d\r\n", i))
		h.assertNoBleed()
	}
}

// TestSurround_ChildRISRepaintsProtectedBar: a full terminal reset from the
// child wipes margins and screen; the region and bar must be re-established
// immediately, and stay protected through subsequent scrolling.
func TestSurround_ChildRISRepaintsProtectedBar(t *testing.T) {
	h := newTearHarness(t, 24, 120)

	h.child("\x1bc")
	h.assertNoBleed()
	for i := 0; i < 30; i++ {
		h.roster("parked")
		h.child(fmt.Sprintf("post-reset line %02d\r\n", i))
		h.assertNoBleed()
	}
	h.feed()
	assert.Contains(t, h.emu.Row(23), barMarker,
		"the bar must be repainted on the reserved row after a child RIS")
}

// TestGate_BarRepaintNeverSplitsChildEscapeSequence: a pty chunk boundary
// falls mid-CSI while a repaint is pending; the repaint must not splice into
// the sequence (the live incident's character-interleaving class).
func TestGate_BarRepaintNeverSplitsChildEscapeSequence(t *testing.T) {
	h := newTearHarness(t, 24, 120)

	h.child("warm")
	h.roster("executing")       // pending repaint
	h.child("x \x1b[38;2;10;2") // chunk ends mid-CSI
	h.child("55mDONE")          // continuation
	assert.Contains(t, h.tty.String(), "\x1b[38;2;10;255m",
		"the child's escape sequence must reach the tty contiguously")
	assertBarPaintsAtGroundBoundaries(t, h.tty.String(), 24)
}

// TestGate_BarRepaintNeverSplitsChildRune: same, with the boundary inside a
// UTF-8 rune.
func TestGate_BarRepaintNeverSplitsChildRune(t *testing.T) {
	h := newTearHarness(t, 24, 120)

	h.child("warm")
	h.roster("executing")
	h.child("q \xe2\x94") // mid-rune │
	h.child("\x82 end")
	assert.Contains(t, h.tty.String(), "\xe2\x94\x82 end",
		"the child's rune bytes must reach the tty contiguously")
	assertBarPaintsAtGroundBoundaries(t, h.tty.String(), 24)
}

// TestSurround_ResizeClobbersChildSavedCursor is the R1 surviving vector: a
// SIGWINCH fires while the child's DECSC is still open. SetSize re-establishes
// the region and repaints the bar — its DECSC-wrapped body paint would overwrite
// the terminal's single saved-cursor slot, so the child's later DECRC restores
// to the wrong cell. The region set and old-row clear are slot-safe and may run;
// only the bar BODY paint must defer while a child save is open.
func TestSurround_ResizeClobbersChildSavedCursor(t *testing.T) {
	h := newTearHarness(t, 24, 120)

	// Child saves its cursor at row 10, col 5 (0-indexed (9,4)) then moves away
	// (so the active cursor differs from the saved slot — the clobber shows).
	h.child("\x1b[10;5H")  // move -> (9,4)
	h.child("\x1b7")       // DECSC: save slot = (9,4)
	h.child("\x1b[20;30H") // move -> (19,29); slot still (9,4)

	// A SIGWINCH grows the terminal while the child's save is open.
	h.c.sur.SetSize(30, 120)

	// Child restores its saved cursor and writes a marker where it saved.
	h.child("\x1b8") // DECRC: intends (9,4)
	h.child("Z")
	h.feed()

	if got := h.emu.Cell(9, 4); got != 'Z' {
		t.Fatalf("child's saved cursor did not survive a resize repaint: marker 'Z' "+
			"landed elsewhere, row10/col5 (0-idx 9,4) = %q; full row 10 = %q",
			string(got), h.emu.Row(9))
	}
}

// assertBarPaintsAtGroundBoundaries verifies every bar repaint in the
// captured stream begins at a parser-ground boundary (never inside a split
// sequence, string, or rune).
func assertBarPaintsAtGroundBoundaries(t *testing.T, out string, rows int) {
	t.Helper()
	sig := fmt.Sprintf("\x1b7\x1b[%d;1H", rows)
	for idx, searched := 0, 0; ; {
		i := strings.Index(out[searched:], sig)
		if i < 0 {
			break
		}
		idx = searched + i
		e := vtemu.New(rows, 80)
		e.Feed([]byte(out[:idx]))
		assert.False(t, e.MidSequence(),
			"bar repaint at offset %d lands mid-sequence: …%q", idx, tail(out[:idx], 24))
		searched = idx + len(sig)
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestSurround_StressScriptedChildNoTearNoBleed hammers the wiring with a
// seeded pseudo-random claude-like stream — SGR runs, CUP redraws, margin
// resets, RIS, OSC titles, UTF-8 — chunked at arbitrary byte boundaries with
// repaints riding busy AND idle paths, then asserts the reserved-row and
// safe-boundary invariants over the whole capture.
func TestSurround_StressScriptedChildNoTearNoBleed(t *testing.T) {
	h := newTearHarness(t, 24, 120)
	rng := rand.New(rand.NewSource(0x5eed))

	var script bytes.Buffer
	for i := 0; i < 400; i++ {
		switch rng.Intn(10) {
		case 0:
			script.WriteString("\x1b[r") // claude-style margin reset
		case 1:
			fmt.Fprintf(&script, "\x1b[%d;%dH\x1b[K", 1+rng.Intn(23), 1+rng.Intn(60))
		case 2:
			fmt.Fprintf(&script, "\x1b[38;2;%d;%d;%dm", rng.Intn(256), rng.Intn(256), rng.Intn(256))
		case 3:
			script.WriteString("\x1b]0;chld title\x07")
		case 4:
			script.WriteString("\x1bc")
		case 5:
			script.WriteString("● chld — status · line │ done\r\n")
		default:
			fmt.Fprintf(&script, "chld %d says things\r\n", i)
		}
	}

	data := script.Bytes()
	for len(data) > 0 {
		n := 1 + rng.Intn(37)
		if n > len(data) {
			n = len(data)
		}
		h.child(string(data[:n]))
		data = data[n:]
		switch rng.Intn(4) {
		case 0:
			h.roster("executing") // busy path: defer to chunk flush
		case 1:
			h.idle()
			h.roster("ended") // idle path: immediate paint
		}
		h.assertNoBleed()
	}
	assertBarPaintsAtGroundBoundaries(t, h.tty.String(), 24)
}
