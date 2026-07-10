package termui

import (
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// SurroundReserve is how many bottom rows the surround bar reserves.
const SurroundReserve = 1

// minRowsForReserve is the terminal height below which reservation is skipped
// entirely (bar off, sizes forwarded untranslated): protecting one row of a
// five-row terminal costs more than it informs. The ResizeTranslator applies
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
	Agent            string
	State            string // queued | executing | parked | idle | ended
	LastActivityUnix int64
}

// Surround owns the reserved bottom row: it establishes a DECSTBM scroll
// region protecting it, paints the status line, and restores the full region
// on exit. All writes go out under the shared tty lock; painting is skipped
// while an overlay is engaged (Suspend) and rides the output gate's
// afterWrite hook when the engine is mid-stream (FlushLocked), so a bar
// repaint never lands inside a split engine escape sequence.
type Surround struct {
	mu      *sync.Mutex // the shared tty lock
	w       io.Writer
	enabled bool
	reserve int
	info    BarInfo

	// engineBusyWindow: a repaint requested while the engine wrote within
	// this window is deferred to the gate's afterWrite flush.
	engineBusyWindow time.Duration
	lastEngineWrite  func() int64 // gate.LastWriteNanos; nil = always paint

	rows, cols int
	active     bool // region established (enabled && big enough)
	suspended  bool
	restored   bool // Restore ran: terminal handed back, never paint again
	roster     []RosterEntry
	hasRoster  bool
	dirty      atomic.Bool
	buf        []byte // render scratch, reused (guarded by mu)
}

// NewSurround builds the bar renderer. mu is the tty lock shared with the
// OutputGate; enabled=false renders nothing ever (reserve stays 0).
func NewSurround(mu *sync.Mutex, w io.Writer, enabled bool, info BarInfo) *Surround {
	reserve := 0
	if enabled {
		reserve = SurroundReserve
	}
	return &Surround{
		mu:               mu,
		w:                w,
		enabled:          enabled,
		reserve:          reserve,
		info:             info,
		engineBusyWindow: 30 * time.Millisecond,
	}
}

// Reserve is the row count the resize translation must subtract.
func (s *Surround) Reserve() int { return s.reserve }

// SetEngineIdle wires the gate's last-write clock for the busy heuristic.
func (s *Surround) SetEngineIdle(lastWriteNanos func() int64) { s.lastEngineWrite = lastWriteNanos }

// SetSize records the REAL terminal size and (re)establishes or releases the
// protected region as the reservation predicate flips. Called on the resize
// translator's goroutine for the initial size and every SIGWINCH.
func (s *Surround) SetSize(rows, cols int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows, s.cols = rows, cols
	if !s.enabled || s.restored {
		return
	}
	if !reserveActive(rows, s.reserve) {
		if s.active {
			s.active = false
			// Terminal shrank below usefulness: hand the full region back.
			_, _ = s.w.Write([]byte("\x1b[r"))
		}
		return
	}
	s.active = true
	// DECSTBM 1..rows-reserve protects the bar rows from engine scrolling;
	// the engine's PTY is (rows-reserve) tall so its cursor addressing never
	// reaches them either. DECSTBM homes the cursor — harmless: the engine
	// repaints on the SIGWINCH that accompanies every size change.
	s.buf = s.buf[:0]
	s.buf = append(s.buf, "\x1b[1;"...)
	s.buf = strconv.AppendInt(s.buf, int64(rows-s.reserve), 10)
	s.buf = append(s.buf, 'r')
	s.buf = s.appendBar(s.buf)
	_, _ = s.w.Write(s.buf)
}

// SetRoster stores the latest children snapshot and requests a repaint —
// immediate when the engine output is idle, deferred to the gate's
// afterWrite flush when mid-stream.
func (s *Surround) SetRoster(roster []RosterEntry) {
	s.mu.Lock()
	s.roster = roster
	s.hasRoster = true
	s.mu.Unlock()
	s.RequestPaint()
}

// RequestPaint repaints now if the engine is idle, else marks the bar dirty
// for the gate's next afterWrite flush.
func (s *Surround) RequestPaint() {
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
func (s *Surround) FlushLocked() {
	if s.dirty.CompareAndSwap(true, false) {
		s.paintLocked()
	}
}

// Suspend stops painting while an overlay owns the screen.
func (s *Surround) Suspend() {
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
func (s *Surround) ResumeSequence() []byte {
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
func (s *Surround) Restore() {
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

// paintLocked renders the bar row. Requires s.mu.
func (s *Surround) paintLocked() {
	if !s.active || s.suspended || s.restored {
		return
	}
	s.buf = s.appendBar(s.buf[:0])
	_, _ = s.w.Write(s.buf)
}

// appendBar appends the full bar paint sequence: save cursor (DECSC), draw
// the bar, restore cursor (DECRC). Single Write, no allocations once the
// scratch has grown.
func (s *Surround) appendBar(b []byte) []byte {
	b = append(b, "\x1b7"...)
	b = s.appendBarBody(b)
	return append(b, "\x1b8"...)
}

// appendBarBody addresses the bar row and draws the reverse-video status
// line padded to the terminal width — no cursor save/restore (callers that
// already own the DECSC slot use this directly).
func (s *Surround) appendBarBody(b []byte) []byte {
	b = append(b, "\x1b["...)
	b = strconv.AppendInt(b, int64(s.rows), 10)
	b = append(b, ";1H\x1b[2K\x1b[7m"...)
	b = appendBarContent(b, s.info, s.rosterDigestLocked(), s.cols)
	return append(b, "\x1b[0m"...)
}

func (s *Surround) rosterDigestLocked() string {
	if !s.hasRoster {
		return ""
	}
	return RosterDigest(s.roster)
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

// RosterDigest summarizes orchestrator-held children for the bar: counts by
// state glyph (● executing, ◐ waiting: queued/parked/idle, ✓ ended) plus the
// latest transition. Empty roster reads "no agents".
func RosterDigest(roster []RosterEntry) string {
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
