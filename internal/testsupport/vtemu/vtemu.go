// Package vtemu is a small xterm screen model for tests: bytes in, a grid of
// cells out. It exists so a test can assert what a terminal would SHOW, not
// which bytes were written — a byte-substring assertion passes on a frame
// that renders as a staircase.
//
// It models the parts of xterm the terminal layer and its bubbletea guest
// drive: cursor addressing and motion, erase and edit, scroll margins with
// region-aware scrolling, autowrap with xterm's pending-wrap column, DECSC/
// DECRC, and the alternate screen. Output processing is the RAW terminal's:
// LF moves down without returning the carriage, which is the whole point for
// a caller that writes to a raw-mode tty.
//
// A sequence it does not model is never silently guessed at. One with no
// effect on the cells (SGR, window ops, mode queries, keyboard protocols) is
// consumed; any other is recorded in Unhandled, so an oracle built on this
// model can refuse to vouch for a frame it did not fully understand.
package vtemu

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type cursor struct{ r, c int }

type buffer struct {
	grid  [][]rune
	saved cursor // DECSC is per buffer in xterm
}

// Screen is the emulated terminal.
type Screen struct {
	rows, cols  int
	main, alt   buffer
	onAlt       bool
	cur         cursor
	wrapPending bool
	autowrap    bool
	top, bot    int // scroll margins, 0-indexed inclusive
	scrollback  []string
	last        rune // for REP
	buf         []byte
	unhandled   map[string]int
}

// New builds a blank rows×cols screen with xterm's power-on modes.
func New(rows, cols int) *Screen {
	s := &Screen{rows: rows, cols: cols, bot: rows - 1, autowrap: true, unhandled: map[string]int{}}
	s.main.grid = blankGrid(rows, cols)
	s.alt.grid = blankGrid(rows, cols)
	return s
}

func blankGrid(rows, cols int) [][]rune {
	g := make([][]rune, rows)
	for i := range g {
		g[i] = blankRow(cols)
	}
	return g
}

func blankRow(cols int) []rune {
	r := make([]rune, cols)
	for i := range r {
		r[i] = ' '
	}
	return r
}

// Rows is the screen height.
func (s *Screen) Rows() int { return s.rows }

// Row is visible row i with trailing blanks trimmed.
func (s *Screen) Row(i int) string { return strings.TrimRight(string(s.grid()[i]), " ") }

// Cell is the rune at visible row r, column c.
func (s *Screen) Cell(r, c int) rune { return s.grid()[r][c] }

// Cursor is the cursor's 0-indexed position.
func (s *Screen) Cursor() (row, col int) { return s.cur.r, s.cur.c }

// OnAltScreen reports whether the alternate screen is displayed.
func (s *Screen) OnAltScreen() bool { return s.onAlt }

// Scrollback is every line scrolled off the top of the main screen.
func (s *Screen) Scrollback() []string { return s.scrollback }

// MidSequence reports whether the bytes fed so far end inside an escape
// sequence, string or partial rune — not a safe boundary to inject at.
func (s *Screen) MidSequence() bool { return len(s.buf) > 0 }

// Unhandled lists the sequences seen that may affect the cells but are not
// modeled, with their counts. Empty means every byte was understood.
func (s *Screen) Unhandled() map[string]int { return s.unhandled }

// String renders the visible screen with row numbers, for failure messages.
func (s *Screen) String() string {
	var b strings.Builder
	for i := 0; i < s.rows; i++ {
		fmt.Fprintf(&b, "%2d|%s\n", i+1, s.Row(i))
	}
	return b.String()
}

// Write feeds p; it never fails.
func (s *Screen) Write(p []byte) (int, error) {
	s.Feed(p)
	return len(p), nil
}

// Feed consumes p, holding back an incomplete tail for the next call.
func (s *Screen) Feed(p []byte) {
	s.buf = append(s.buf, p...)
	for len(s.buf) > 0 {
		n := s.step(s.buf)
		if n == 0 {
			return
		}
		s.buf = s.buf[n:]
	}
}

func (s *Screen) grid() [][]rune {
	if s.onAlt {
		return s.alt.grid
	}
	return s.main.grid
}

func (s *Screen) buffer() *buffer {
	if s.onAlt {
		return &s.alt
	}
	return &s.main
}

func (s *Screen) step(b []byte) int {
	switch b[0] {
	case 0x1b:
		return s.stepEscape(b)
	case '\n', '\v', '\f':
		s.lineFeed()
		return 1
	case '\r':
		s.moveTo(s.cur.r, 0)
		return 1
	case '\b':
		s.moveTo(s.cur.r, s.cur.c-1)
		return 1
	case '\t':
		s.moveTo(s.cur.r, min((s.cur.c/8+1)*8, s.cols-1))
		return 1
	}
	if b[0] < 0x20 || b[0] == 0x7f {
		return 1 // BEL and the other C0 controls move nothing
	}
	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError && !utf8.FullRune(b) {
		return 0
	}
	s.put(r)
	return size
}

// put writes one printable rune with xterm's autowrap: writing the last
// column leaves the cursor there with a wrap pending, and only the NEXT
// printable wraps (CR+LF, scrolling at the margin).
func (s *Screen) put(r rune) {
	if s.wrapPending {
		s.wrapPending = false
		if s.autowrap {
			s.cur.c = 0
			s.lineFeed()
		}
	}
	s.grid()[s.cur.r][s.cur.c] = r
	s.last = r
	if s.cur.c < s.cols-1 {
		s.cur.c++
	} else {
		s.wrapPending = true
	}
}

// moveTo positions the cursor, clamped, cancelling a pending wrap as every
// cursor motion does.
func (s *Screen) moveTo(r, c int) {
	s.cur = cursor{clamp(r, 0, s.rows-1), clamp(c, 0, s.cols-1)}
	s.wrapPending = false
}

// escActions are the two-byte escapes that act on the screen.
var escActions = map[byte]func(*Screen){
	'7': func(s *Screen) { s.buffer().saved = s.cur },
	'8': func(s *Screen) { sv := s.buffer().saved; s.moveTo(sv.r, sv.c) },
	'c': (*Screen).reset,
	'D': (*Screen).lineFeed,
	'E': func(s *Screen) { s.cur.c = 0; s.lineFeed() },
	'M': (*Screen).reverseIndex,
	'=': func(*Screen) {}, // keypad modes: no cell effect
	'>': func(*Screen) {},
}

func (s *Screen) stepEscape(b []byte) int {
	if len(b) < 2 {
		return 0
	}
	switch {
	case b[1] == '[':
		return s.stepCSI(b)
	case strings.IndexByte("]PX^_", b[1]) >= 0:
		return skipString(b)
	case b[1] >= 0x20 && b[1] <= 0x2f: // ESC intermediate final (charset designation)
		if len(b) < 3 {
			return 0
		}
		return 3
	}
	if act, ok := escActions[b[1]]; ok {
		act(s)
	} else {
		s.unhandled[fmt.Sprintf("ESC %q", b[1])]++
	}
	return 2
}

// reset is RIS. It resets the terminal, not this model's bookkeeping: the
// unread input Feed is walking and the Unhandled record survive it.
func (s *Screen) reset() {
	pending, seen := s.buf, s.unhandled
	*s = *New(s.rows, s.cols)
	s.buf, s.unhandled = pending, seen
}

// skipString consumes an OSC/DCS/APC/PM/SOS string up to BEL or ST.
func skipString(b []byte) int {
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
}

// csi is one parsed control sequence.
type csi struct {
	prefix, inter string
	final         byte
	params        []string
}

// n is parameter i, with def standing in for an absent or zero value.
func (q csi) n(i, def int) int {
	if i >= len(q.params) || q.params[i] == "" {
		return def
	}
	v := 0
	for _, ch := range q.params[i] {
		if ch < '0' || ch > '9' {
			return def
		}
		v = v*10 + int(ch-'0')
	}
	if v == 0 {
		return def
	}
	return v
}

// span returns the end of the run of bytes in [lo, hi] starting at i.
func span(b []byte, i int, lo, hi byte) int {
	for i < len(b) && b[i] >= lo && b[i] <= hi {
		i++
	}
	return i
}

func parseCSI(b []byte) (csi, int) {
	p := span(b, 2, 0x3c, 0x3f)   // private prefix
	pe := span(b, p, 0x30, 0x3b)  // parameters
	ie := span(b, pe, 0x20, 0x2f) // intermediates
	if ie >= len(b) {
		return csi{}, 0
	}
	return csi{
		prefix: string(b[2:p]),
		params: strings.Split(string(b[p:pe]), ";"),
		inter:  string(b[pe:ie]),
		final:  b[ie],
	}, ie + 1
}

func (s *Screen) stepCSI(b []byte) int {
	q, n := parseCSI(b)
	if n == 0 {
		return 0
	}
	switch {
	case q.prefix == "?" && q.inter == "" && (q.final == 'h' || q.final == 'l'):
		s.privateModes(q)
	case q.prefix == "" && q.inter == "":
		s.plainCSI(q)
	case isInert(q):
	default:
		s.unhandled[fmt.Sprintf("CSI %s%s%c", q.prefix, q.inter, q.final)]++
	}
	return n
}

// inert is the prefixed/intermediate sequences with no effect on cells,
// keyed prefix+intermediates+final: DECRQM queries, keyboard-protocol
// pushes/pops/queries, modifyOtherKeys, cursor style.
var inert = map[string]bool{
	"$p": true, "?$p": true,
	"=u": true, ">u": true, "<u": true, "?u": true,
	">m": true,
	" q": true,
}

func isInert(q csi) bool { return inert[q.prefix+q.inter+string(q.final)] }

func (s *Screen) privateModes(q csi) {
	set := q.final == 'h'
	for i := range q.params {
		switch q.n(i, 0) {
		case 7:
			s.autowrap = set
		case 47:
			s.switchScreen(set, false, false)
		case 1047:
			s.switchScreen(set, false, true)
		case 1049:
			s.switchScreen(set, true, true)
		case 1, 12, 25, 1000, 1002, 1003, 1004, 1006, 1015, 2004, 2026, 2027:
			// keypad, blink, visibility, mouse, focus, paste, sync, grapheme:
			// no effect on the cells
		default:
			s.unhandled[fmt.Sprintf("CSI ?%s%c", q.params[i], q.final)]++
		}
	}
}

// switchScreen implements 47/1047/1049 as xterm does. The saved cursor is
// per buffer: 1049 saves into the buffer it is leaving on entry and restores
// from the main buffer after switching back. 1049 clears the alternate screen
// on entry, 1047 on exit, 47 never. Entering while already there, or leaving
// while not, switches nothing — but 1049 still saves/restores.
func (s *Screen) switchScreen(toAlt, cursorSave, clear bool) {
	if toAlt {
		if cursorSave {
			s.buffer().saved = s.cur
		}
		if !s.onAlt {
			s.onAlt = true
			if clear && cursorSave {
				s.alt.grid = blankGrid(s.rows, s.cols)
			}
		}
		return
	}
	if s.onAlt {
		if clear && !cursorSave {
			s.alt.grid = blankGrid(s.rows, s.cols)
		}
		s.onAlt = false
	}
	if cursorSave {
		sv := s.buffer().saved
		s.moveTo(sv.r, sv.c)
	}
}

// csiActions are the unprefixed control sequences, by final byte.
var csiActions = map[byte]func(*Screen, csi){
	'H': func(s *Screen, q csi) { s.moveTo(q.n(0, 1)-1, q.n(1, 1)-1) },
	'A': func(s *Screen, q csi) { s.moveTo(s.cur.r-q.n(0, 1), s.cur.c) },
	'B': func(s *Screen, q csi) { s.moveTo(s.cur.r+q.n(0, 1), s.cur.c) },
	'C': func(s *Screen, q csi) { s.moveTo(s.cur.r, s.cur.c+q.n(0, 1)) },
	'D': func(s *Screen, q csi) { s.moveTo(s.cur.r, s.cur.c-q.n(0, 1)) },
	'E': func(s *Screen, q csi) { s.moveTo(s.cur.r+q.n(0, 1), 0) },
	'F': func(s *Screen, q csi) { s.moveTo(s.cur.r-q.n(0, 1), 0) },
	'G': func(s *Screen, q csi) { s.moveTo(s.cur.r, q.n(0, 1)-1) },
	'd': func(s *Screen, q csi) { s.moveTo(q.n(0, 1)-1, s.cur.c) },
	'r': (*Screen).setMargins,
	'K': func(s *Screen, q csi) { s.eraseLine(q.n(0, 0)) },
	'J': func(s *Screen, q csi) { s.eraseScreen(q.n(0, 0)) },
	'X': func(s *Screen, q csi) { s.fill(s.cur.r, s.cur.c, min(s.cur.c+q.n(0, 1), s.cols)) },
	'@': func(s *Screen, q csi) { s.shiftRow(q.n(0, 1), true) },
	'P': func(s *Screen, q csi) { s.shiftRow(q.n(0, 1), false) },
	'L': func(s *Screen, q csi) { s.insertLines(q.n(0, 1)) },
	'M': func(s *Screen, q csi) { s.deleteLines(q.n(0, 1)) },
	'S': func(s *Screen, q csi) { repeat(q.n(0, 1), s.scrollUp) },
	'T': func(s *Screen, q csi) { repeat(q.n(0, 1), s.scrollDown) },
	'b': func(s *Screen, q csi) { repeat(q.n(0, 1), func() { s.put(s.last) }) },
	's': func(s *Screen, _ csi) { s.buffer().saved = s.cur },
	'u': func(s *Screen, _ csi) { sv := s.buffer().saved; s.moveTo(sv.r, sv.c) },
	'h': (*Screen).ansiModes,
	'l': (*Screen).ansiModes,
	// SGR, window ops, reports: no cell effect
	'm': func(*Screen, csi) {},
	't': func(*Screen, csi) {},
	'n': func(*Screen, csi) {},
	'c': func(*Screen, csi) {},
	'q': func(*Screen, csi) {},
}

func init() {
	csiActions['f'] = csiActions['H']
	csiActions['e'] = csiActions['B']
	csiActions['a'] = csiActions['C']
	csiActions['`'] = csiActions['G']
}

func repeat(n int, f func()) {
	for range n {
		f()
	}
}

func (s *Screen) plainCSI(q csi) {
	if act, ok := csiActions[q.final]; ok {
		act(s, q)
		return
	}
	s.unhandled[fmt.Sprintf("CSI %c", q.final)]++
}

func (s *Screen) setMargins(q csi) {
	t, bo := q.n(0, 1), q.n(1, s.rows)
	if t < bo && bo <= s.rows {
		s.top, s.bot = t-1, bo-1
		s.moveTo(0, 0)
	}
}

// ansiModes: insert (4) and newline (20) change what bytes do to the cells,
// and are not modeled; the others have no cell effect.
func (s *Screen) ansiModes(q csi) {
	for i := range q.params {
		if v := q.n(i, 0); v == 4 || v == 20 {
			s.unhandled[fmt.Sprintf("CSI %d%c", v, q.final)]++
		}
	}
}

func (s *Screen) fill(r, from, to int) {
	row := s.grid()[r]
	for i := from; i < to; i++ {
		row[i] = ' '
	}
	s.wrapPending = false
}

func (s *Screen) eraseLine(mode int) {
	switch mode {
	case 2:
		s.fill(s.cur.r, 0, s.cols)
	case 1:
		s.fill(s.cur.r, 0, s.cur.c+1)
	default:
		s.fill(s.cur.r, s.cur.c, s.cols)
	}
}

func (s *Screen) eraseScreen(mode int) {
	g := s.grid()
	switch mode {
	case 3:
		s.scrollback = nil
	case 2:
		for i := range g {
			g[i] = blankRow(s.cols)
		}
	case 1:
		for i := 0; i < s.cur.r; i++ {
			g[i] = blankRow(s.cols)
		}
		s.eraseLine(1)
	default:
		s.eraseLine(0)
		for i := s.cur.r + 1; i < s.rows; i++ {
			g[i] = blankRow(s.cols)
		}
	}
}

func (s *Screen) shiftRow(n int, insert bool) {
	row := s.grid()[s.cur.r]
	c := s.cur.c
	n = min(n, s.cols-c)
	if insert {
		copy(row[c+n:], row[c:s.cols-n])
		s.fill(s.cur.r, c, c+n)
		return
	}
	copy(row[c:], row[c+n:])
	s.fill(s.cur.r, s.cols-n, s.cols)
}

func (s *Screen) inRegion() bool { return s.cur.r >= s.top && s.cur.r <= s.bot }

func (s *Screen) insertLines(n int) {
	if !s.inRegion() {
		return
	}
	g := s.grid()
	for range min(n, s.bot-s.cur.r+1) {
		copy(g[s.cur.r+1:s.bot+1], g[s.cur.r:s.bot])
		g[s.cur.r] = blankRow(s.cols)
	}
	s.moveTo(s.cur.r, 0)
}

func (s *Screen) deleteLines(n int) {
	if !s.inRegion() {
		return
	}
	g := s.grid()
	for range min(n, s.bot-s.cur.r+1) {
		copy(g[s.cur.r:s.bot], g[s.cur.r+1:s.bot+1])
		g[s.bot] = blankRow(s.cols)
	}
	s.moveTo(s.cur.r, 0)
}

// scrollUp scrolls the region up one line; only a full-screen region on the
// main screen feeds the scrollback, as in xterm.
func (s *Screen) scrollUp() {
	g := s.grid()
	if !s.onAlt && s.top == 0 && s.bot == s.rows-1 {
		s.scrollback = append(s.scrollback, strings.TrimRight(string(g[0]), " "))
	}
	copy(g[s.top:s.bot], g[s.top+1:s.bot+1])
	g[s.bot] = blankRow(s.cols)
}

func (s *Screen) scrollDown() {
	g := s.grid()
	copy(g[s.top+1:s.bot+1], g[s.top:s.bot])
	g[s.top] = blankRow(s.cols)
}

func (s *Screen) lineFeed() {
	s.wrapPending = false
	switch {
	case s.cur.r == s.bot:
		s.scrollUp()
	case s.cur.r < s.rows-1:
		s.cur.r++
	}
}

func (s *Screen) reverseIndex() {
	s.wrapPending = false
	switch {
	case s.cur.r == s.top:
		s.scrollDown()
	case s.cur.r > 0:
		s.cur.r--
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
