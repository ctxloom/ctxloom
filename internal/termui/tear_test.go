package termui

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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
// stream, then replay the captured tty bytes through a small VT emulator and
// assert the invariants a live terminal needs:
//
//   - bar content only ever renders on the reserved bottom row (never above
//     it, never in scrollback);
//   - child escape sequences and runes arrive contiguously (no bar bytes
//     spliced into their middle);
//   - every bar repaint begins at a parser-ground boundary.

// ---------------------------------------------------------------------------
// Mini VT emulator: just enough xterm to render the surround + a child stream
// (CUP, EL, ED, DECSTBM incl. region-aware LF scrolling, DECSC/DECRC, RIS,
// OSC skip, SGR ignore). Lines scroll into scrollback only when the scroll
// region is the full screen, matching xterm.
// ---------------------------------------------------------------------------

type vtEmu struct {
	rows, cols int
	grid       [][]rune
	curR, curC int
	top, bot   int // scroll margins, 0-indexed inclusive
	savR, savC int
	scrollback []string
	buf        []byte // undecoded tail (mid-sequence / mid-rune)
}

func newVTEmu(rows, cols int) *vtEmu {
	e := &vtEmu{rows: rows, cols: cols, bot: rows - 1}
	e.grid = make([][]rune, rows)
	for i := range e.grid {
		e.grid[i] = blankRow(cols)
	}
	return e
}

func blankRow(cols int) []rune {
	r := make([]rune, cols)
	for i := range r {
		r[i] = ' '
	}
	return r
}

func (e *vtEmu) row(i int) string { return strings.TrimRight(string(e.grid[i]), " ") }

// midSequence reports whether the fed bytes end inside an escape sequence,
// string, or partial rune — i.e. NOT a safe boundary for an injected paint.
func (e *vtEmu) midSequence() bool { return len(e.buf) > 0 }

func (e *vtEmu) Feed(p []byte) {
	e.buf = append(e.buf, p...)
	for len(e.buf) > 0 {
		n := e.step(e.buf)
		if n == 0 {
			return // incomplete tail: wait for more bytes
		}
		e.buf = e.buf[n:]
	}
}

// step consumes one action from b, returning bytes consumed (0 = incomplete).
func (e *vtEmu) step(b []byte) int {
	switch b[0] {
	case 0x1b:
		return e.stepEscape(b)
	case '\n':
		e.lineFeed()
		return 1
	case '\r':
		e.curC = 0
		return 1
	case '\b':
		if e.curC > 0 {
			e.curC--
		}
		return 1
	}
	if b[0] < 0x20 {
		return 1 // other C0: ignore
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && !utf8.FullRune(b) {
		return 0
	}
	e.grid[e.curR][e.curC] = r
	if e.curC < e.cols-1 {
		e.curC++ // clamp at the last column (no autowrap modeling needed)
	}
	return size
}

func (e *vtEmu) stepEscape(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	switch b[1] {
	case '[':
		return e.stepCSI(b)
	case ']', 'P', 'X', '^', '_': // OSC/DCS/…: skip to BEL or ST
		for i := 2; i < len(b); i++ {
			if b[i] == 0x07 {
				return i + 1
			}
			if b[i] == 0x1b {
				if i+1 >= len(b) {
					return 0
				}
				if b[i+1] == '\\' {
					return i + 2
				}
				return i // aborted: reprocess the ESC
			}
		}
		return 0
	case '7':
		e.savR, e.savC = e.curR, e.curC
		return 2
	case '8':
		e.curR, e.curC = e.savR, e.savC
		return 2
	case 'c': // RIS
		for i := range e.grid {
			e.grid[i] = blankRow(e.cols)
		}
		e.curR, e.curC, e.savR, e.savC = 0, 0, 0, 0
		e.top, e.bot = 0, e.rows-1
		return 2
	}
	if b[1] >= 0x20 && b[1] <= 0x2f { // ESC + intermediate + final
		if len(b) < 3 {
			return 0
		}
		return 3
	}
	return 2 // other two-byte escapes: ignore
}

func (e *vtEmu) stepCSI(b []byte) int {
	end := -1
	for i := 2; i < len(b); i++ {
		if b[i] >= 0x40 && b[i] <= 0x7e {
			end = i
			break
		}
	}
	if end < 0 {
		return 0
	}
	body := string(b[2:end])
	private := strings.ContainsAny(body, "<=>?")
	params := strings.Split(body, ";")
	num := func(i, def int) int {
		if i >= len(params) || params[i] == "" {
			return def
		}
		v := 0
		for _, c := range params[i] {
			if c < '0' || c > '9' {
				return def
			}
			v = v*10 + int(c-'0')
		}
		if v == 0 {
			return def
		}
		return v
	}
	if !private {
		switch b[end] {
		case 'H', 'f':
			e.curR = clamp(num(0, 1)-1, 0, e.rows-1)
			e.curC = clamp(num(1, 1)-1, 0, e.cols-1)
		case 'A':
			e.curR = clamp(e.curR-num(0, 1), 0, e.rows-1)
		case 'B':
			e.curR = clamp(e.curR+num(0, 1), 0, e.rows-1)
		case 'C':
			e.curC = clamp(e.curC+num(0, 1), 0, e.cols-1)
		case 'D':
			e.curC = clamp(e.curC-num(0, 1), 0, e.cols-1)
		case 'r':
			t, bo := num(0, 1), num(1, e.rows)
			if t >= 1 && bo <= e.rows && t < bo {
				e.top, e.bot = t-1, bo-1
				e.curR, e.curC = 0, 0
			}
		case 'K':
			e.eraseLine(num(0, 0))
		case 'J':
			e.eraseScreen(num(0, 0))
		}
	}
	return end + 1
}

func (e *vtEmu) eraseLine(mode int) {
	row := e.grid[e.curR]
	switch mode {
	case 2:
		e.grid[e.curR] = blankRow(e.cols)
	case 1:
		for i := 0; i <= e.curC; i++ {
			row[i] = ' '
		}
	default:
		for i := e.curC; i < e.cols; i++ {
			row[i] = ' '
		}
	}
}

func (e *vtEmu) eraseScreen(mode int) {
	switch mode {
	case 2, 3:
		for i := range e.grid {
			e.grid[i] = blankRow(e.cols)
		}
	case 1:
		for i := 0; i < e.curR; i++ {
			e.grid[i] = blankRow(e.cols)
		}
		e.eraseLine(1)
	default:
		e.eraseLine(0)
		for i := e.curR + 1; i < e.rows; i++ {
			e.grid[i] = blankRow(e.cols)
		}
	}
}

func (e *vtEmu) lineFeed() {
	if e.curR == e.bot {
		// Scroll the region; only a full-screen region feeds the scrollback.
		if e.top == 0 && e.bot == e.rows-1 {
			e.scrollback = append(e.scrollback, e.row(e.top))
		}
		copy(e.grid[e.top:e.bot], e.grid[e.top+1:e.bot+1])
		e.grid[e.bot] = blankRow(e.cols)
		return
	}
	if e.curR < e.rows-1 {
		e.curR++
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

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
	emu   *vtEmu
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
	src := make(chan *pb.WindowSize, 1)
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
	src <- &pb.WindowSize{Rows: uint32(rows), Cols: uint32(cols)}
	waitFor(t, "surround establish", func() bool {
		return strings.Contains(tty.String(), fmt.Sprintf("\x1b[1;%dr", rows-1))
	})
	h := &tearHarness{t: t, c: c, tty: tty, emu: newVTEmu(rows, cols), now: &now}
	h.clean = func() {
		nowNanos = restoreNow
		_ = pw.Close()
		close(src)
		c.Close()
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
	h.c.sur.SetRoster([]RosterEntry{{Harp: "sixth-royal-kelp", Agent: "finder", State: state, LastActivityUnix: 1}})
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
	for i := 0; i < h.emu.rows-1; i++ {
		if strings.Contains(h.emu.row(i), barMarker) {
			h.t.Fatalf("bar content leaked into the scrolling region: row %d = %q", i+1, h.emu.row(i))
		}
	}
	for _, l := range h.emu.scrollback {
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
	assert.Contains(t, h.emu.row(23), barMarker,
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
		e := newVTEmu(rows, 80)
		e.Feed([]byte(out[:idx]))
		assert.False(t, e.midSequence(),
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
