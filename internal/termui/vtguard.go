package termui

import "strconv"

// vtGuard is the child-output stage of the tty writer: every engine byte bound
// for the real terminal flows through Filter, which does three things the
// surround's reserved row cannot survive without:
//
//  1. Region defense — a child that resets the scroll margins (its own
//     DECSTBM/`\x1b[r`, RIS, DECSTR, alt-screen exit) silently unprotects the
//     reserved bottom row; from then on every scroll drags torn bar copies
//     into the scrollback. DECSTBM bottoms are clamped IN-STREAM to the
//     child's viewport (the child's "full screen" IS rows−reserve, so the
//     clamp translates its intent exactly — cursor homing included), and the
//     sequences that can't be rewritten (RIS, DECSTR, alt-screen leave) get
//     the region re-assert + bar repaint inserted immediately after them.
//
//  2. Safe paint boundaries — engine chunks arrive at pty-read boundaries,
//     which routinely split escape sequences and UTF-8 runes. A trailing
//     incomplete ESC/CSI (bounded) or rune tail is held back until the next
//     chunk completes it, so the written stream always ends at a boundary a
//     bar repaint may follow. While inside an unboundable string (OSC/DCS —
//     titles, OSC 52 clipboard) the stream passes through and SafeForPaint
//     reports false, deferring repaints until the string terminates.
//
//  3. Damage notice — full-screen erases (ED 2/3, alt-screen enter) wipe the
//     bar row without touching the margins; they mark the bar dirty so the
//     gate's afterWrite flush repaints it on the same write cycle.
//
// The guard is not goroutine-safe: the outputGate calls it under the shared
// tty lock, the same lock the surround paints under.
type vtGuard struct {
	// regionBottom returns the last protected-region row (rows−reserve) while
	// the reservation is active, else 0 (no clamping, no inserts). Called with
	// the tty lock held.
	regionBottom func() int
	// reassert returns the region re-assert + bar repaint bytes (nil when the
	// surround has nothing to paint). Called with the tty lock held.
	reassert func() []byte
	// barDamaged marks the bar dirty after a screen-clearing sequence.
	barDamaged func()

	state  vtState
	seq    []byte // pending incomplete ESC/CSI bytes, held across chunks
	tail   []byte // pending incomplete UTF-8 rune, re-emitted first next chunk
	oscBel bool   // current string mode also terminates on BEL (OSC does)
	torn   bool   // written stream ends in a child-torn sequence fragment
	// childSaved tracks the child's DECSC (ESC 7) save into the terminal's
	// SINGLE saved-cursor slot: true from an ESC 7 until the matching ESC 8
	// (DECRC) releases it. This is one slot, not a depth — a second ESC 7 keeps
	// it set, an ESC 8 (or a RIS, which resets the slot) clears it. While set,
	// SafeForPaint defers bar paints, whose own DECSC would otherwise overwrite
	// the child's saved cell and make its later DECRC restore to the wrong spot.
	// SYNC-REQUIRED: written in Filter and read by SafeForPaint (the surround's
	// paint gate), both under the shared tty lock — keep all access on that lock.
	childSaved bool
	out        []byte // output scratch, reused; returned slice valid until next call
}

type vtState int

const (
	vtGround    vtState = iota
	vtEsc               // after ESC (plus any intermediates)
	vtCSI               // after ESC [ — buffering params/intermediates
	vtString            // inside OSC/DCS/SOS/PM/APC — streamed through
	vtStringEsc         // ESC seen inside a string: ST or an aborting sequence
	vtGarbage           // oversized CSI overflowed the buffer — raw until final
)

// vtCSIMax bounds the pending-CSI buffer. Real CSIs (SGR truecolor chains
// included) sit far below this; anything longer flushes raw and rides out in
// garbage mode so child output can never stall behind the guard.
const vtCSIMax = 96

func newVTGuard(regionBottom func() int, reassert func() []byte, barDamaged func()) *vtGuard {
	return &vtGuard{regionBottom: regionBottom, reassert: reassert, barDamaged: barDamaged}
}

// SafeForPaint reports whether the bytes written so far end at a boundary a
// bar repaint may follow: everything except mid-string (OSC/DCS), the
// overflowed-CSI escape valve, a child-torn fragment, and — the DECSC guard —
// an outstanding child cursor save. A pending held-back ESC/CSI is safe — those
// bytes were never written, so the terminal's parser sits in ground state.
func (g *vtGuard) SafeForPaint() bool {
	return g.state != vtString && g.state != vtStringEsc && g.state != vtGarbage &&
		!g.torn && !g.childSaved
}

// Filter transforms one child chunk into the bytes to write. The returned
// slice is reused scratch — write it before the next call.
func (g *vtGuard) Filter(p []byte) []byte {
	g.out = append(g.out[:0], g.tail...)
	g.tail = g.tail[:0]
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch g.state {
		case vtGround:
			if b == 0x1b {
				g.state = vtEsc
				g.seq = append(g.seq[:0], b)
			} else {
				g.out = append(g.out, b)
			}
		case vtEsc:
			g.stepEsc(b)
		case vtCSI:
			g.stepCSI(b)
		case vtString:
			g.stepString(b)
		case vtStringEsc:
			g.stepStringEsc(b)
		case vtGarbage:
			g.stepGarbage(b)
		}
	}
	if g.state == vtGround {
		g.holdRuneTail()
	}
	return g.out
}

// stepEsc consumes the byte after ESC (or after ESC + intermediates).
func (g *vtGuard) stepEsc(b byte) {
	switch {
	case b == '[':
		g.seq = append(g.seq, b)
		g.state = vtCSI
	case b == ']':
		// OSC: emit the prefix and stream the (unboundable) string through.
		g.emitSeq(b)
		g.state = vtString
		g.oscBel = true
	case b == 'P' || b == 'X' || b == '^' || b == '_':
		// DCS/SOS/PM/APC: like OSC but ST-terminated only.
		g.emitSeq(b)
		g.state = vtString
		g.oscBel = false
	case b == '7':
		// DECSC: the child saved its cursor into the terminal's single slot.
		// Hold off bar paints (their own DECSC would overwrite it) until the
		// child's DECRC releases the slot.
		g.emitSeq(b)
		g.childSaved = true
		g.state = vtGround
	case b == '8':
		// DECRC: the child restored (and freed) its saved cursor; the slot is
		// available for a bar paint again.
		g.emitSeq(b)
		g.childSaved = false
		g.state = vtGround
	case b == 'c':
		// RIS: full reset — margins gone, screen cleared, cursor homed, saved
		// slot reset. The child expects a virgin terminal; re-establishing the
		// region and bar right here is the standing translation (DECSTBM
		// re-homes, a no-op).
		g.emitSeq(b)
		g.childSaved = false
		g.insertReassert()
		g.state = vtGround
	case b == 0x1b:
		// ESC ESC restarts the sequence; emit the first so the stream stays
		// byte-faithful. Until the held ESC's sequence resolves, the written
		// stream ends mid-escape.
		g.out = append(g.out, 0x1b)
		g.torn = true
	case b >= 0x20 && b <= 0x2f:
		g.seq = append(g.seq, b) // intermediate (e.g. ESC # 8); final follows
	case b == 0x18 || b == 0x1a:
		// CAN/SUB abort the sequence; the terminal re-aborts identically.
		g.emitSeq(b)
		g.state = vtGround
	case b < 0x20:
		g.out = append(g.out, b) // C0 executes without disturbing the sequence
	default:
		g.emitSeq(b) // two/three-byte escape complete (ESC 7, ESC 8, ESC =, …)
		g.state = vtGround
	}
}

// stepCSI buffers CSI bytes until the final, then dispatches.
func (g *vtGuard) stepCSI(b byte) {
	switch {
	case b >= 0x40 && b <= 0x7e:
		g.seq = append(g.seq, b)
		g.finishCSI()
		g.state = vtGround
	case b == 0x1b:
		// ESC aborts the CSI and opens a new sequence; emit the torn bytes so
		// the terminal aborts the same way. Until the held ESC's sequence is
		// written, the tty genuinely sits mid-CSI: no paint boundary.
		g.out = append(g.out, g.seq...)
		g.seq = append(g.seq[:0], b)
		g.torn = true
		g.state = vtEsc
	case b == 0x18 || b == 0x1a:
		g.emitSeq(b)
		g.state = vtGround
	case b < 0x20:
		g.out = append(g.out, b) // C0 executes mid-CSI
	default:
		g.seq = append(g.seq, b)
		if len(g.seq) > vtCSIMax {
			// Not a CSI we can reason about: flush raw, ride it out.
			g.out = append(g.out, g.seq...)
			g.seq = g.seq[:0]
			g.state = vtGarbage
		}
	}
}

func (g *vtGuard) stepString(b byte) {
	switch {
	case b == 0x07 && g.oscBel:
		g.out = append(g.out, b)
		g.state = vtGround
	case b == 0x1b:
		g.state = vtStringEsc
	case b == 0x18 || b == 0x1a:
		g.out = append(g.out, b)
		g.state = vtGround
	default:
		g.out = append(g.out, b)
	}
}

func (g *vtGuard) stepStringEsc(b byte) {
	if b == '\\' {
		// ST: the string terminates.
		g.out = append(g.out, 0x1b, b)
		g.state = vtGround
		return
	}
	// The ESC aborted the string and starts a new sequence with this byte;
	// the held ESC becomes that sequence's introducer (emitting it here too
	// would double it on the tty). Until it is written the tty still sits
	// inside the unterminated string.
	g.seq = append(g.seq[:0], 0x1b)
	g.torn = true
	g.state = vtEsc
	g.stepEsc(b)
}

func (g *vtGuard) stepGarbage(b byte) {
	if b == 0x1b {
		g.seq = append(g.seq[:0], b)
		g.state = vtEsc
		return
	}
	g.out = append(g.out, b)
	if b >= 0x40 && b <= 0x7e {
		g.state = vtGround
	}
}

// emitSeq flushes the pending sequence plus its final byte. Writing a
// terminated sequence returns the tty to ground even after a torn fragment
// (its leading ESC aborts whatever the tear left open).
func (g *vtGuard) emitSeq(final byte) {
	g.out = append(g.out, g.seq...)
	g.out = append(g.out, final)
	g.seq = g.seq[:0]
	g.torn = false
}

// finishCSI dispatches one complete CSI held in g.seq (ESC '[' … final).
func (g *vtGuard) finishCSI() {
	seq := g.seq
	final := seq[len(seq)-1]
	body := seq[2 : len(seq)-1]
	private := len(body) > 0 && body[0] >= 0x3c && body[0] <= 0x3f
	intermediate := byte(0)
	for _, c := range body {
		if c >= 0x20 && c <= 0x2f {
			intermediate = c
		}
	}
	bottom := 0
	if g.regionBottom != nil {
		bottom = g.regionBottom()
	}
	switch {
	case final == 'r' && !private && intermediate == 0 && bottom > 0:
		// DECSTBM: clamp the bottom margin to the child's viewport. `\x1b[r`
		// (and any bottom ≥ the reserved row) means "my full screen", which
		// IS rows−reserve — the rewrite preserves the child's intent, homing
		// included, and the reserved row never joins the scrolling region.
		g.rewriteDECSTBM(body, bottom)
		g.seq = g.seq[:0]
		return
	case final == 'p' && intermediate == '!' && bottom > 0:
		// DECSTR soft reset: margins reset to full screen, active cursor
		// untouched — but the saved-cursor slot is reset, so a child DECSC left
		// open no longer holds its cell. Clear childSaved (as RIS does) so it
		// can't starve paints.
		g.emitPending()
		g.childSaved = false
		g.insertReassert()
		return
	case (final == 'l' || final == 'h') && private && csiParamsContainAltScreen(body[1:]):
		g.emitPending()
		if final == 'l' {
			// Leaving the alt screen restores/consumes the main-screen
			// saved-cursor slot (1049l), so a child DECSC left open no longer
			// holds its cell — clear childSaved lest it starve paints.
			g.childSaved = false
			if bottom > 0 {
				// The alt app may have left the main-screen margins anywhere;
				// re-assert and repaint.
				g.insertReassert()
			} else if g.barDamaged != nil {
				g.barDamaged()
			}
		} else if g.barDamaged != nil {
			// Entering (1049 clears the alt screen): the bar row starts blank
			// there; the margins carry over, so a repaint is all it needs.
			g.barDamaged()
		}
		return
	case final == 'J' && !private && intermediate == 0 && csiFirstParam(body, 0) >= 2:
		// ED 2/3 erase the full screen, bar row included, margins untouched.
		g.emitPending()
		if g.barDamaged != nil {
			g.barDamaged()
		}
		return
	}
	g.emitPending()
}

// emitPending flushes the buffered (complete) CSI unchanged.
func (g *vtGuard) emitPending() {
	g.out = append(g.out, g.seq...)
	g.seq = g.seq[:0]
	g.torn = false
}

// rewriteDECSTBM emits the clamped scroll-region set.
func (g *vtGuard) rewriteDECSTBM(params []byte, bottom int) {
	g.torn = false
	top := csiParamAt(params, 0)
	bot := csiParamAt(params, 1)
	if top < 1 {
		top = 1
	}
	if bot < 1 || bot > bottom {
		bot = bottom
	}
	g.out = append(g.out, "\x1b["...)
	g.out = strconv.AppendInt(g.out, int64(top), 10)
	g.out = append(g.out, ';')
	g.out = strconv.AppendInt(g.out, int64(bot), 10)
	g.out = append(g.out, 'r')
}

// insertReassert stitches the region re-assert + bar repaint into the output
// stream right behind the sequence that clobbered it.
func (g *vtGuard) insertReassert() {
	if g.reassert == nil {
		return
	}
	g.out = append(g.out, g.reassert()...)
}

// holdRuneTail moves a trailing incomplete UTF-8 rune from out to tail so a
// bar repaint after this chunk can never split a glyph.
func (g *vtGuard) holdRuneTail() {
	n := len(g.out)
	if n == 0 || g.out[n-1] < 0x80 {
		return
	}
	// Find a lead byte within the last 3 bytes.
	for back := 1; back <= 3 && back <= n; back++ {
		b := g.out[n-back]
		if b < 0x80 {
			return // ASCII boundary: the trailing bytes are orphaned junk
		}
		if b < 0xc0 {
			continue // continuation byte, keep looking for the lead
		}
		want := 2
		switch {
		case b >= 0xf0:
			want = 4
		case b >= 0xe0:
			want = 3
		}
		if back < want {
			g.tail = append(g.tail[:0], g.out[n-back:]...)
			g.out = g.out[:n-back]
		}
		return
	}
}

// csiParamAt parses the i-th semicolon-separated numeric parameter; 0 when
// absent/empty (the DECSTBM "default" marker).
func csiParamAt(params []byte, i int) int {
	idx, v := 0, 0
	for _, c := range params {
		switch {
		case c == ';':
			if idx == i {
				return v
			}
			idx++
			v = 0
		case c >= '0' && c <= '9':
			if idx == i {
				v = v*10 + int(c-'0')
			}
		}
	}
	if idx == i {
		return v
	}
	return 0
}

// csiFirstParam parses the leading numeric parameter with a default.
func csiFirstParam(params []byte, def int) int {
	if len(params) == 0 {
		return def
	}
	v, seen := 0, false
	for _, c := range params {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int(c-'0')
		seen = true
	}
	if !seen {
		return def
	}
	return v
}

// csiParamsContainAltScreen reports whether a private-mode parameter list
// names an alt-screen mode (47, 1047, 1049).
func csiParamsContainAltScreen(params []byte) bool {
	v, seen := 0, false
	check := func() bool { return seen && (v == 47 || v == 1047 || v == 1049) }
	for _, c := range params {
		switch {
		case c >= '0' && c <= '9':
			v = v*10 + int(c-'0')
			seen = true
		case c == ';':
			if check() {
				return true
			}
			v, seen = 0, false
		}
	}
	return check()
}
