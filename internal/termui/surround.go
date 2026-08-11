package termui

import (
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// surroundReserve is how many bottom rows the surround bar reserves.
const surroundReserve = 1

// minRowsForReserve is the terminal height below which reservation is skipped
// entirely (bar off, sizes forwarded untranslated): protecting one row of a
// five-row terminal costs more than it informs. The resizeTranslator applies
// the same predicate so the engine viewport and the protected region always
// agree.
const minRowsForReserve = 6

// reserveActive is the single reservation predicate shared by the surround
// and the resize translation.
func reserveActive(rows, reserve int) bool {
	return reserve > 0 && rows >= minRowsForReserve
}

// nowNanos is a seam for tests; production is time.Now.
var nowNanos = func() int64 { return time.Now().UnixNano() }

// BarInfo is the static identity segment of the surround bar.
type BarInfo struct {
	Harp   string
	Agent  string
	Engine string
	Model  string
	// PrefixHint is the rendered key hint (CaretHint of the prefix byte).
	PrefixHint string
}

// RosterEntry is the surround's view of one coordinator-held child —
// deliberately a local mirror of coord.RosterEntry (internal/agentcoord/
// coord; agentbus retired in D2) so this hot-path package stays
// dependency-light and hermetically testable.
type RosterEntry struct {
	Harp             string
	State            string // queued | executing | parked | idle | ended
	LastActivityUnix int64
}

// surround owns the reserved bottom row: it establishes a DECSTBM scroll
// region protecting it, paints the status line, and restores the full region
// on exit. All writes go out under the shared tty lock; painting is skipped
// while an overlay is engaged (Suspend) and rides the output gate's
// afterWrite hook when the engine is mid-stream (FlushLocked), so a bar
// repaint never lands inside a split engine escape sequence.
type surround struct {
	mu      *sync.Mutex // the shared tty lock
	w       io.Writer
	enabled bool
	reserve int
	info    BarInfo

	// engineBusyWindow: a repaint requested while the engine wrote within
	// this window is deferred to the gate's afterWrite flush.
	engineBusyWindow time.Duration
	lastEngineWrite  func() int64 // gate.LastWriteNanos; nil = always paint

	// paintSafe reports whether the tty byte stream currently ends at a
	// boundary a bar paint may follow (the gate guard's SafeForPaint). A
	// paint attempted mid-stream re-marks dirty instead. nil = always safe.
	// Called with mu held.
	paintSafe func() bool

	rows, cols int
	active     bool // region established (enabled && big enough)
	suspended  bool
	restored   bool // Restore ran: terminal handed back, never paint again
	roster     []RosterEntry
	hasRoster  bool
	// approvals is the last known count of approvals parked for the human
	// (Controller.Options.FetchApprovals). Tracked separately from roster so
	// SetApprovals can detect the 0→N transition that rings the bell without
	// caring whether SetRoster has ever been called.
	approvals    int
	hasApprovals bool
	dirty        atomic.Bool
	buf          []byte // render scratch, reused (guarded by mu)
}

// newSurround builds the bar renderer. mu is the tty lock shared with the
// outputGate; enabled=false renders nothing ever (reserve stays 0).
func newSurround(mu *sync.Mutex, w io.Writer, enabled bool, info BarInfo) *surround {
	reserve := 0
	if enabled {
		reserve = surroundReserve
	}
	return &surround{
		mu:               mu,
		w:                w,
		enabled:          enabled,
		reserve:          reserve,
		info:             info,
		engineBusyWindow: 30 * time.Millisecond,
	}
}

// regionBottomLocked is the guard's clamp bound: the last protected-region
// row while the reservation stands, else 0 (child owns the full screen — no
// clamping). Requires s.mu.
func (s *surround) regionBottomLocked() int {
	if !s.active || s.restored {
		return 0
	}
	return s.rows - s.reserve
}

// reassertLocked returns the bytes the guard inserts right behind a child
// sequence that clobbered the region (RIS, DECSTR, alt-screen leave): scroll
// region re-established and the bar repainted, wrapped in DECSC/DECRC so the
// child's cursor position survives the DECSTBM home. Requires s.mu.
func (s *surround) reassertLocked() []byte {
	if !s.active || s.suspended || s.restored {
		return nil
	}
	b := make([]byte, 0, 128)
	b = append(b, "\x1b7\x1b[1;"...)
	b = strconv.AppendInt(b, int64(s.rows-s.reserve), 10)
	b = append(b, 'r')
	b = s.appendBarBody(b)
	return append(b, "\x1b8"...)
}

// markDirtyLocked flags the bar for the gate's next afterWrite flush — the
// guard's notice that a child sequence erased the bar row (ED 2/3, alt-screen
// enter) without touching the margins. Requires s.mu (same write cycle).
func (s *surround) markDirtyLocked() { s.dirty.Store(true) }

// SetSize records the REAL terminal size and (re)establishes or releases the
// protected region as the reservation predicate flips. Called on the resize
// translator's goroutine for the initial size and every SIGWINCH.
//
// While an overlay is suspending the bar (Suspend), the overlay owns the
// whole screen (the controller reset the scroll region to full on engage), so
// a SIGWINCH arriving mid-engagement must not write anything here — a region
// re-assert or bar repaint would land directly on the live overlay. The size
// (and the reservation-active flag it drives) is still recorded so
// ResumeSequence and the resize translator pick up the latest dimensions;
// the actual region+bar paint is deferred to ResumeSequence on release.
func (s *surround) SetSize(rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prevRows := s.rows
	wasActive := s.active
	s.rows, s.cols = rows, cols
	if !s.enabled || s.restored {
		return
	}
	if !reserveActive(rows, s.reserve) || cols <= 0 {
		// cols<=0 must be treated the same as a too-short terminal —
		// reservation honestly OFF — rather than establishing the region and
		// having fitWidth silently discard every byte of bar content, which
		// painted a structurally-valid but completely empty bar row with no
		// warning.
		if s.active {
			s.active = false
			if !s.suspended {
				// Terminal shrank below usefulness: hand the full region back.
				_, _ = s.w.Write([]byte("\x1b[r"))
			}
		}
		return
	}
	s.active = true
	if s.suspended {
		return
	}
	// DECSTBM 1..rows-reserve protects the bar rows from engine scrolling;
	// the engine's PTY is (rows-reserve) tall so its cursor addressing never
	// reaches them either. DECSTBM homes the cursor — harmless: the engine
	// repaints on the SIGWINCH that accompanies every size change.
	s.buf = s.buf[:0]
	// A resize moves the reserved bottom row: the previous bar row is left
	// stranded — inside the new region on a grow, dropped below it on a shrink —
	// so clear it before establishing the new region, otherwise the old bar
	// generation lingers on the primary screen. Addressed absolutely (CUP
	// ignores margins), it clears while the row is still reachable. Both the
	// clear and the region set (DECSTBM) touch only the ACTIVE cursor, never the
	// terminal's SAVED-cursor slot, so they are safe to emit unconditionally.
	if wasActive && prevRows > 0 && prevRows != rows {
		s.buf = append(s.buf, "\x1b["...)
		s.buf = strconv.AppendInt(s.buf, int64(prevRows), 10)
		s.buf = append(s.buf, ";1H\x1b[2K"...)
	}
	s.buf = append(s.buf, "\x1b[1;"...)
	s.buf = strconv.AppendInt(s.buf, int64(rows-s.reserve), 10)
	s.buf = append(s.buf, 'r')
	// Only the DECSC-wrapped bar BODY paint clobbers the single saved-cursor
	// slot. SYNC-REQUIRED: paintSafe (the guard's SafeForPaint, read under this
	// same tty lock) reports false while a child DECSC is open; skip the body
	// paint then and mark dirty so the existing SafeForPaint-gated flush repaints
	// once the child's DECRC closes the slot — never clobber an open child save.
	if s.paintSafe == nil || s.paintSafe() {
		s.buf = s.appendBar(s.buf)
	} else {
		s.dirty.Store(true)
	}
	_, _ = s.w.Write(s.buf)
}

// SetRoster stores the latest children snapshot and requests a repaint —
// immediate when the engine output is idle, deferred to the gate's
// afterWrite flush when mid-stream.
func (s *surround) SetRoster(roster []RosterEntry) {
	s.mu.Lock()
	s.roster = roster
	s.hasRoster = true
	s.mu.Unlock()
	s.RequestPaint()
}

// SetApprovals stores the latest pending-approval count and requests a
// repaint, exactly like SetRoster. On a 0→N transition — ONLY that
// transition, never a later N→N tick reporting the same nonzero count — it
// also writes one BEL byte directly under the shared tty lock so a
// disengaged human hears it (the surround bar is the one surface visible
// while the overlay isn't engaged, per the slice 3 plan's two-surface
// reality). The bell is a standalone C0 control byte, not part of any
// escape/CSI sequence the output gate's guard tracks, so it is safe to write
// unconditionally under mu rather than routing through paintSafe.
func (s *surround) SetApprovals(n int) {
	s.mu.Lock()
	bell := n > 0 && s.approvals == 0
	s.approvals = n
	s.hasApprovals = true
	if bell && s.active && !s.suspended && !s.restored {
		_, _ = s.w.Write([]byte{'\a'})
	}
	s.mu.Unlock()
	s.RequestPaint()
}

// RequestPaint repaints now if the engine is idle, else marks the bar dirty
// for the gate's next afterWrite flush.
func (s *surround) RequestPaint() {
	if s.lastEngineWrite != nil {
		if since := nowNanos() - s.lastEngineWrite(); since < int64(s.engineBusyWindow) {
			s.dirty.Store(true)
			return
		}
	}
	s.mu.Lock()
	s.paintLocked()
	s.mu.Unlock()
}

// FlushLocked is the gate's afterWrite hook: repaint a dirty bar between
// engine output chunks. Requires the tty lock (held by the gate).
func (s *surround) FlushLocked() {
	if s.dirty.CompareAndSwap(true, false) {
		s.paintLocked()
	}
}

// Suspend stops painting while an overlay owns the screen.
func (s *surround) Suspend() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suspended = true
}

// ResumeSequence re-enables painting and RETURNS the resume bytes — the
// re-established scroll region (the overlay ran with the full screen) plus a
// bar repaint — instead of writing them. The controller stitches them into
// the output gate's release preamble so region, bar, cursor restore, and the
// held-output replay hit the tty as one atomic write, with no engine bytes
// interleaving. The bar paint here deliberately avoids DECSC/DECRC: the
// single save slot still holds the engine's engage-time cursor, which the
// controller restores right after.
func (s *surround) ResumeSequence() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.suspended = false
	if !s.active || s.restored {
		return nil
	}
	b := make([]byte, 0, 64)
	b = append(b, "\x1b[1;"...)
	b = strconv.AppendInt(b, int64(s.rows-s.reserve), 10)
	b = append(b, 'r')
	return s.appendBarBody(b)
}

// Restore hands the terminal back on exit: full scroll region, bar row
// cleared, cursor parked on the cleared bottom row so the shell prompt lands
// clean. Idempotent; after it the surround never paints again.
func (s *surround) Restore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restored {
		return
	}
	s.restored = true
	if !s.active {
		return
	}
	s.active = false
	s.buf = s.buf[:0]
	s.buf = append(s.buf, "\x1b[r\x1b["...)
	s.buf = strconv.AppendInt(s.buf, int64(s.rows), 10)
	s.buf = append(s.buf, ";1H\x1b[2K"...)
	_, _ = s.w.Write(s.buf)
}

// paintLocked renders the bar row. Requires s.mu. A paint attempted while the
// child stream ends mid-sequence (inside an OSC/DCS string) re-marks the bar
// dirty for the next safe flush instead of tearing the sequence.
func (s *surround) paintLocked() {
	if !s.active || s.suspended || s.restored {
		return
	}
	if s.paintSafe != nil && !s.paintSafe() {
		s.dirty.Store(true)
		return
	}
	s.buf = s.appendBar(s.buf[:0])
	_, _ = s.w.Write(s.buf)
}

// appendBar appends the full bar paint sequence: save cursor (DECSC), draw
// the bar, restore cursor (DECRC). Single Write, no allocations once the
// scratch has grown.
func (s *surround) appendBar(b []byte) []byte {
	b = append(b, "\x1b7"...)
	b = s.appendBarBody(b)
	return append(b, "\x1b8"...)
}

// appendBarBody addresses the bar row and draws the reverse-video status
// line padded to the terminal width — no cursor save/restore (callers that
// already own the DECSC slot use this directly).
func (s *surround) appendBarBody(b []byte) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(s.rows), 10)
	b = append(b, ";1H\x1b[2K\x1b[7m"...)
	b = appendBarContent(b, s.info, s.rosterDigestLocked(), s.cols)
	return append(b, "\x1b[0m"...)
}

func (s *surround) rosterDigestLocked() string {
	if !s.hasRoster && !s.hasApprovals {
		return ""
	}
	return rosterDigest(s.roster, s.approvals)
}

// appendBarContent renders ` harp · agent · engine/model │ digest │ hint `
// truncated (by rune) and space-padded to width.
func appendBarContent(b []byte, info BarInfo, digest string, width int) []byte {
	start := len(b)
	b = append(b, ' ')
	b = append(b, info.Harp...)
	if info.Agent != "" {
		b = append(b, " · "...)
		b = append(b, info.Agent...)
	}
	if info.Engine != "" {
		b = append(b, " · "...)
		b = append(b, info.Engine...)
		if info.Model != "" {
			b = append(b, '/')
			b = append(b, info.Model...)
		}
	}
	if digest != "" {
		b = append(b, " │ "...)
		b = append(b, digest...)
	}
	if info.PrefixHint != "" {
		b = append(b, " │ "...)
		b = append(b, info.PrefixHint...)
		b = append(b, " viewer"...)
	}
	b = append(b, ' ')
	return fitWidth(b, start, width)
}

// fitWidth rune-truncates or space-pads b[start:] to exactly width cells.
// Bar content is single-width text (the state glyphs included), so rune
// count stands in for display width.
func fitWidth(b []byte, start, width int) []byte {
	if width <= 0 {
		return b[:start]
	}
	runes := utf8.RuneCount(b[start:])
	for runes > width {
		_, size := utf8.DecodeLastRune(b)
		b = b[:len(b)-size]
		runes--
	}
	for ; runes < width; runes++ {
		b = append(b, ' ')
	}
	return b
}

// rosterDigest summarizes orchestrator-held children for the bar: counts by
// state glyph (● executing, ◐ waiting: queued/parked/idle, ✓ ended) plus the
// latest transition, prefixed with a "⚠N " approval warning when N (the
// human's pending-approval count) is greater than zero — always the LEADING
// element, since it names the thing most likely to need the human's
// attention right now. Empty roster reads "no agents".
func rosterDigest(roster []RosterEntry, approvals int) string {
	body := rosterDigestBody(roster)
	if approvals <= 0 {
		return body
	}
	return "⚠" + strconv.Itoa(approvals) + " " + body
}

// rosterDigestBody is the roster-only half of rosterDigest, unchanged from
// before the approvals warning was added.
func rosterDigestBody(roster []RosterEntry) string {
	if len(roster) == 0 {
		return "no agents"
	}
	var running, waiting, done int
	latest := roster[0]
	for _, r := range roster {
		switch r.State {
		case "executing":
			running++
		case "ended":
			done++
		default:
			waiting++
		}
		if r.LastActivityUnix > latest.LastActivityUnix {
			latest = r
		}
	}
	b := make([]byte, 0, 48)
	b = strconv.AppendInt(b, int64(running), 10)
	b = append(b, "● "...)
	b = strconv.AppendInt(b, int64(waiting), 10)
	b = append(b, "◐ "...)
	b = strconv.AppendInt(b, int64(done), 10)
	b = append(b, "✓ · "...)
	b = append(b, latest.Harp...)
	b = append(b, "→"...)
	b = append(b, latest.State...)
	return string(b)
}
