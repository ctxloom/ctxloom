package termui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// newTestGuard returns a guard clamping to bottom row 23 that inserts
// "<REASSERT>" and counts damage marks.
func newTestGuard(bottom int) (*vtGuard, *int) {
	damaged := 0
	g := newVTGuard(
		func() int { return bottom },
		func() []byte { return []byte("<REASSERT>") },
		func() { damaged++ },
	)
	return g, &damaged
}

// filterAll runs the whole input through in one chunk.
func filterAll(g *vtGuard, s string) string {
	return string(g.Filter([]byte(s)))
}

func TestVTGuard_ClampsDECSTBM(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"bare reset", "\x1b[r", "\x1b[1;23r"},
		{"zero params", "\x1b[0;0r", "\x1b[1;23r"},
		{"empty params", "\x1b[;r", "\x1b[1;23r"},
		{"bottom beyond viewport", "\x1b[5;30r", "\x1b[5;23r"},
		{"child full viewport", "\x1b[1;23r", "\x1b[1;23r"},
		{"inner region untouched", "\x1b[5;20r", "\x1b[5;20r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newTestGuard(23)
			assert.Equal(t, tc.want, filterAll(g, tc.in))
		})
	}
}

func TestVTGuard_LeavesNonDECSTBMAlone(t *testing.T) {
	g, _ := newTestGuard(23)
	// Private-mode 'r' (XTRESTORE) and inactive reservation must pass raw.
	assert.Equal(t, "\x1b[?5r", filterAll(g, "\x1b[?5r"))
	inactive, _ := newTestGuard(0)
	assert.Equal(t, "\x1b[r", filterAll(inactive, "\x1b[r"),
		"no reservation → the child owns the full screen, nothing is clamped")
}

func TestVTGuard_ReassertsAfterClobbers(t *testing.T) {
	cases := []struct{ name, in, wantAfter string }{
		{"RIS", "\x1bc", "\x1bc<REASSERT>"},
		{"DECSTR", "\x1b[!p", "\x1b[!p<REASSERT>"},
		{"alt-screen leave", "\x1b[?1049l", "\x1b[?1049l<REASSERT>"},
		{"legacy alt leave", "\x1b[?47l", "\x1b[?47l<REASSERT>"},
		{"multi-mode leave", "\x1b[?2004;1049l", "\x1b[?2004;1049l<REASSERT>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, _ := newTestGuard(23)
			assert.Equal(t, "pre|"+tc.wantAfter+"|post", filterAll(g, "pre|"+tc.in+"|post"),
				"the re-assert must land immediately behind the clobbering sequence")
		})
	}
}

func TestVTGuard_MarksBarDamagedOnScreenClears(t *testing.T) {
	// U141-F03: ED 0 (bare "\x1b[J", the default) and explicit "\x1b[0J" erase
	// from the cursor to the END of the display — which reaches the reserved
	// bar row exactly like ED 2/3 do, since ED ignores scroll margins. Only
	// ED 1 (erase to cursor) is confined above the child's own viewport and is
	// genuinely safe.
	for _, in := range []string{"\x1b[2J", "\x1b[3J", "\x1b[?1049h", "\x1b[J", "\x1b[0J"} {
		g, damaged := newTestGuard(23)
		out := filterAll(g, in)
		assert.Equal(t, in, out, "damage sequences pass through unmodified")
		assert.Equal(t, 1, *damaged, "%q must mark the bar dirty", in)
	}
	g, damaged := newTestGuard(23)
	_ = filterAll(g, "\x1b[1J")
	assert.Zero(t, *damaged, "ED 1 (erase to cursor) never touches the bar row")
}

func TestVTGuard_HoldsBackSplitSequencesAndRunes(t *testing.T) {
	g, _ := newTestGuard(23)

	// CSI split across three chunks: nothing mid-sequence ever reaches the tty.
	assert.Equal(t, "color ", filterAll(g, "color \x1b[38;2;1"))
	assert.True(t, g.SafeForPaint(), "held-back CSI bytes were never written — the tty is at ground")
	assert.Equal(t, "", filterAll(g, "0;25"))
	assert.Equal(t, "\x1b[38;2;10;255m txt", filterAll(g, "5m txt"))

	// UTF-8 rune split at the chunk boundary.
	assert.Equal(t, "box ", filterAll(g, "box \xe2\x94"))
	assert.Equal(t, "\xe2\x94\x82 done", filterAll(g, "\x82 done"))

	// Lone trailing ESC.
	assert.Equal(t, "x", filterAll(g, "x\x1b"))
	assert.Equal(t, "\x1b[2K", filterAll(g, "[2K"))
}

func TestVTGuard_UnsafeInsideStrings(t *testing.T) {
	g, _ := newTestGuard(23)
	assert.Equal(t, "\x1b]0;my ti", filterAll(g, "\x1b]0;my ti"),
		"string contents stream through — they are unboundable")
	assert.False(t, g.SafeForPaint(), "mid-OSC is not a paint boundary")
	assert.Equal(t, "tle\x07after", filterAll(g, "tle\x07after"))
	assert.True(t, g.SafeForPaint())

	// ST-terminated variant, terminator split across chunks.
	assert.Equal(t, "\x1bPq data", filterAll(g, "\x1bPq data"))
	assert.False(t, g.SafeForPaint())
	assert.Equal(t, "", filterAll(g, "\x1b"))
	assert.Equal(t, "\x1b\\ok", filterAll(g, "\\ok"))
	assert.True(t, g.SafeForPaint())
}

func TestVTGuard_OversizedCSIFlushesRaw(t *testing.T) {
	g, _ := newTestGuard(23)
	huge := "\x1b[" + strings.Repeat("1;", 60) + "m"
	out := filterAll(g, huge[:len(huge)-1])
	assert.True(t, len(out) > 0, "an overflowed CSI must flush rather than stall")
	assert.False(t, g.SafeForPaint(), "riding out an overflowed CSI is not a paint boundary")
	out += filterAll(g, "m")
	assert.Equal(t, huge, out, "the bytes still arrive verbatim")
	assert.True(t, g.SafeForPaint())
}

func TestVTGuard_ByteFidelityUnderArbitraryChunking(t *testing.T) {
	// A benign claude-like stream must come out byte-identical no matter how
	// it is chunked (only DECSTBM resets/RIS/alt-leave are ever rewritten).
	src := "plain text\r\n\x1b[2K\x1b[7mreverse\x1b[0m │ box ● glyphs\r\n" +
		"\x1b]0;title\x07\x1b[5;10H\x1b[38;2;1;2;3mcolor\x1b[m\x1b7save\x1b8\x1b[1;23r\r\n"
	for chunk := 1; chunk <= len(src); chunk++ {
		g, _ := newTestGuard(23)
		var out bytes.Buffer
		for i := 0; i < len(src); i += chunk {
			end := i + chunk
			if end > len(src) {
				end = len(src)
			}
			out.Write(g.Filter([]byte(src[i:end])))
		}
		assert.Equal(t, src, out.String(), "chunk size %d", chunk)
	}
}

// TestVTGuard_ChildDECSCSlotLifecycle pins the single-slot DECSC guard: an open
// child save (ESC 7) blocks paints, a DECRC (ESC 8) reopens them, a second ESC 7
// keeps it closed (not a depth), and a RIS resets the slot. Byte output stays
// verbatim throughout — only the paint-boundary verdict changes.
func TestVTGuard_ChildDECSCSlotLifecycle(t *testing.T) {
	g, _ := newTestGuard(23)
	assert.True(t, g.SafeForPaint(), "ground: paints allowed")

	assert.Equal(t, "\x1b7", filterAll(g, "\x1b7"))
	assert.False(t, g.SafeForPaint(), "open child DECSC blocks a bar paint")

	// A second DECSC is still one slot — the flag stays set, not a stack.
	assert.Equal(t, "\x1b7", filterAll(g, "\x1b7"))
	assert.False(t, g.SafeForPaint(), "a second DECSC keeps the slot occupied")

	assert.Equal(t, "\x1b8", filterAll(g, "\x1b8"))
	assert.True(t, g.SafeForPaint(), "one DECRC releases the slot")

	// RIS resets the saved-cursor slot even with a save outstanding.
	filterAll(g, "\x1b7")
	assert.False(t, g.SafeForPaint())
	filterAll(g, "\x1bc")
	assert.True(t, g.SafeForPaint(), "RIS clears the saved-cursor slot")
}

// TestVTGuard_ChildDECSCClearedBySoftResetAndAltLeave is the R2 starvation
// guard: a soft reset (DECSTR) and an alt-screen leave (1049l) both reset/consume
// the terminal's saved-cursor slot, so a child DECSC left open before them no
// longer holds a cell. The guard must clear childSaved on those paths or it
// starves every bar paint until a far-off DECRC/RIS.
func TestVTGuard_ChildDECSCClearedBySoftResetAndAltLeave(t *testing.T) {
	t.Run("DECSTR", func(t *testing.T) {
		g, _ := newTestGuard(23)
		filterAll(g, "\x1b7")
		assert.False(t, g.SafeForPaint(), "open child DECSC blocks paints")
		filterAll(g, "\x1b[!p") // DECSTR: resets the saved-cursor slot
		assert.True(t, g.SafeForPaint(),
			"DECSTR resets the saved slot; childSaved must clear, not starve paints")
	})
	t.Run("alt-screen-leave", func(t *testing.T) {
		g, _ := newTestGuard(23)
		filterAll(g, "\x1b7")
		assert.False(t, g.SafeForPaint())
		filterAll(g, "\x1b[?1049l") // leave alt screen: restores/consumes the slot
		assert.True(t, g.SafeForPaint(),
			"alt-screen leave consumes the saved slot; childSaved must clear")
	})
}

// TestVTGuard_ChildSavedTracksANSISysSpelling pins U141-F05: the ANSI.SYS
// save/restore spelling (CSI s / CSI u) writes into the SAME single
// saved-cursor slot as DECSC/DECRC (ESC 7/8) — a child using this spelling
// must block bar paints exactly like ESC 7 does, or the bar's own DECSC
// overwrites the child's saved cell and the child's later CSI u restores to
// the wrong position.
func TestVTGuard_ChildSavedTracksANSISysSpelling(t *testing.T) {
	g, _ := newTestGuard(23)
	assert.True(t, g.SafeForPaint())
	assert.Equal(t, "\x1b[s", filterAll(g, "\x1b[s"))
	assert.False(t, g.SafeForPaint(), "CSI s occupies the saved-cursor slot like ESC 7")
	assert.Equal(t, "\x1b[u", filterAll(g, "\x1b[u"))
	assert.True(t, g.SafeForPaint(), "CSI u releases the slot like ESC 8")
}

// TestVTGuard_StepEscIntermediatesBounded pins U141-F06: stepEsc appended
// escape intermediates (0x20-0x2f) to g.seq with no length bound, unlike
// stepCSI's vtCSIMax escape valve — a stray ESC followed by a long run of
// intermediate bytes (e.g. spaces) buffered unboundedly and withheld
// everything from the tty until a final byte arrived.
func TestVTGuard_StepEscIntermediatesBounded(t *testing.T) {
	g, _ := newTestGuard(23)
	long := "\x1b" + strings.Repeat(" ", vtCSIMax+10)
	out := filterAll(g, long)
	assert.True(t, len(out) > 0, "an oversized escape-intermediate run must flush rather than buffer unboundedly")
	out += filterAll(g, "q")
	assert.Equal(t, long+"q", out, "bytes still arrive verbatim once ridden out")
}

// TestVTGuard_ChildDECSCClearedBySoftResetEvenWhenBarInactive pins U141-F12:
// the DECSTR arm was gated on bottom>0, so when the bar is inactive (short
// terminal, surround off) a soft reset did not clear childSaved even though
// DECSTR resets the terminal's saved-cursor slot — if the reservation later
// becomes active, the stale flag starves every bar paint.
func TestVTGuard_ChildDECSCClearedBySoftResetEvenWhenBarInactive(t *testing.T) {
	g, _ := newTestGuard(0) // bar inactive: bottom==0
	filterAll(g, "\x1b7")
	assert.False(t, g.SafeForPaint(), "open child DECSC blocks paints")
	filterAll(g, "\x1b[!p") // DECSTR while the bar is inactive
	assert.True(t, g.SafeForPaint(), "DECSTR must clear childSaved even when bottom==0")
}

func TestVTGuard_AbortedStringReprocessesEscape(t *testing.T) {
	g, _ := newTestGuard(23)
	// An ESC inside an OSC that is NOT an ST aborts the string and starts a
	// new sequence — here a DECSTBM reset that must still be clamped.
	assert.Equal(t, "\x1b]0;tit\x1b[1;23r", filterAll(g, "\x1b]0;tit\x1b[r"))
	assert.True(t, g.SafeForPaint())
}
