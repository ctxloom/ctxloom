package termui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// This file reproduces two defects observed in a live session whose surround
// bar (harp "aloof-mean-stove") tore child text and left multiple bar
// generations stacked in the NORMAL scrollback after `exit`:
//
//	expected `⏵⏵ bypass permissions on`  → rendered `✻ ⏵⏵obypassrpermissions on`
//	expected `Through July 12, you can …` → rendered `SThroughoJulye12,oyoutcana…`
//	plus a `wired-huge-usage …` bar and an `aloof-mean-stove …` bar both in history.
//
// H1 (tearing) — the bar paint's DECSC/DECRC (`\x1b7 … \x1b8`) uses the
// terminal's SINGLE saved-cursor slot. A child that opens its own DECSC in one
// chunk and closes it (DECRC) in a later chunk has its saved cursor silently
// overwritten by any bar paint interposed in the gap, so the child's DECRC
// restores to the wrong cell and subsequent child text lands scrambled.
//
// H2 (stacked bars) — no code path clears a bar row when the bar's row changes
// or the region is reset; only the *current* reserved row is ever cleared. A
// resize therefore leaves a stale bar generation on the primary screen (and, on
// a shrink, the terminal pushes it into scrollback).

// ---------------------------------------------------------------------------
// H1: cross-chunk child DECSC + interposed bar paint corrupts the restore.
// ---------------------------------------------------------------------------

// TestSurround_BarPaintClobbersChildSavedCursor drives the REAL gate/surround/
// vtGuard wiring: the child saves its cursor (DECSC) at a known cell, moves
// away, and — with a bar repaint interposed between the save and the restore —
// restores (DECRC) and writes a marker. The marker must land at the saved cell.
// It does not, because the interposed bar paint's own DECSC overwrote the
// terminal's single saved-cursor slot.
func TestSurround_BarPaintClobbersChildSavedCursor(t *testing.T) {
	h := newTearHarness(t, 24, 120)

	// Child saves its cursor at row 10, col 5 (0-indexed (9,4)).
	h.child("\x1b[10;5H") // move -> (9,4)
	h.child("\x1b7")      // DECSC: save slot = (9,4)

	// Child moves elsewhere to draw transient output.
	h.child("\x1b[20;30H") // move -> (19,29)

	// A roster change requests a repaint; the engine is "busy" (frozen clock),
	// so the paint defers to the gate's afterWrite flush of the NEXT chunk —
	// i.e. it fires AFTER these bytes, while the child's DECSC is still open.
	h.roster("executing")
	h.child("draw") // afterWrite flushes the bar here (its DECSC clobbers the slot)

	// Child restores its saved cursor and writes a marker where it saved.
	h.child("\x1b8") // DECRC: intends (9,4), gets the clobbered slot
	h.child("Z")
	h.feed()

	if got := h.emu.grid[9][4]; got != 'Z' {
		t.Fatalf("child's saved cursor did not survive an interposed bar paint: "+
			"marker 'Z' landed elsewhere, row10/col5 (0-idx 9,4) = %q; full row 10 = %q",
			string(got), h.emu.row(9))
	}
}

// TestVTGuard_SafeForPaintIgnoresOpenChildDECSC pins the root-cause fact: the
// guard does not track the child's saved-cursor slot, so SafeForPaint() reports
// a paint boundary even while the child has an outstanding DECSC. A bar paint
// admitted here overwrites that slot. The assertion encodes the *fix contract*
// (a paint must not be admitted while a child save is outstanding) and so fails
// against today's guard, which returns true.
func TestVTGuard_SafeForPaintIgnoresOpenChildDECSC(t *testing.T) {
	g, _ := newTestGuard(23)

	// Child moves, opens a DECSC, keeps streaming — but never DECRCs.
	out := filterAll(g, "\x1b[10;5H\x1b7still typing")
	assert.Equal(t, "\x1b[10;5H\x1b7still typing", out,
		"the child bytes pass through verbatim (only the save-slot state matters)")

	assert.False(t, g.SafeForPaint(),
		"a bar paint admitted while the child's DECSC is open clobbers the "+
			"terminal's single saved-cursor slot; SafeForPaint must defer until the "+
			"child's DECRC closes it")
}

// ---------------------------------------------------------------------------
// H2: a resize leaves a stale bar generation — nothing clears the old bar row.
// ---------------------------------------------------------------------------

// TestSurround_ResizeGrowLeavesStaleBarGeneration grows the terminal from 24 to
// 30 rows. SetSize re-establishes the region one row higher and paints a fresh
// bar on the new bottom row, but never clears the OLD bar row: two bar
// generations end up co-resident on the primary screen. On a shrink the same
// gap is worse — the terminal pushes the displaced bar row into scrollback,
// which is how multiple bar generations came to be stacked in the incident's
// history. Only the reserved bottom row is ever cleared, so no path removes a
// displaced generation.
func TestSurround_ResizeGrowLeavesStaleBarGeneration(t *testing.T) {
	var tty bytes.Buffer
	s := newTestSurround(&tty, BarInfo{Harp: "aloof-mean-stove", PrefixHint: "^]"})

	s.SetSize(24, 80) // bar painted on row 24
	s.SetSize(30, 80) // grow: region 1;29, new bar on row 30 — row 24 left as-is

	emu := newVTEmu(30, 80)
	emu.Feed(tty.Bytes())

	var barRows []int
	for i := 0; i < emu.rows; i++ {
		if strings.Contains(emu.row(i), barMarker) {
			barRows = append(barRows, i+1)
		}
	}
	assert.Equal(t, []int{30}, barRows,
		"after a grow only the new bottom-row bar may remain; the old bar row "+
			"(row 24) was never cleared, so two bar generations coexist")
}
