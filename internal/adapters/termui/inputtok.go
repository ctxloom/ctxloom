package termui

import (
	"bytes"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// tokKind classifies one whole unit of terminal INPUT.
type tokKind int

const (
	// tokKey is anything the human typed: a rune, a control byte, a key's
	// escape sequence.
	tokKey tokKind = iota
	// tokReport is the terminal answering a query (CPR, DA, DSR, DECRPM, focus
	// in/out, kitty flags, OSC/DCS/APC replies). The engine asked, so the
	// engine gets it, whoever holds focus.
	tokReport
	// tokMouse is a mouse report the engine's tracking mode asked for.
	tokMouse
	tokPasteBegin
	tokPasteData
	tokPasteEnd
)

// maxPendingInput bounds the tail carried while a sequence is incomplete. A
// real reply (an OSC 52 clipboard read included) fits well inside; one that
// never ends is let go as a key rather than growing the carry forever.
const maxPendingInput = 64 << 10

var pasteEnd = []byte("\x1b[201~")

// inputTok splits the raw stdin stream into whole tokens, carrying an
// incomplete one across reads, and tracks whether the stream sits at an
// input boundary: ground state, outside a bracketed paste. That is the only
// place focus may move without splitting a sequence or a paste between the
// engine and the modal. Classification uses x/ansi's decoder, fed from the
// start of each token so the decoder always sees the prefix that types it.
// Not goroutine-safe: the interceptor calls it under its own lock.
type inputTok struct {
	pend    []byte // an incomplete token's bytes so far
	inPaste bool
	scratch []byte
}

// atBoundary reports whether focus may move here. A carried ESC, or ESC plus
// one byte (Alt+[, Alt+O …, which also open sequences), was a key once the
// stream has been quiet: a terminal writes a sequence in one burst, never
// across a pause.
func (t *inputTok) atBoundary(quiet bool) bool {
	if t.inPaste {
		return false
	}
	return len(t.pend) == 0 || (quiet && len(t.pend) <= 2 && t.pend[0] == ansi.ESC)
}

// track advances the boundary state over bytes that go straight to the
// engine. The common read — plain typing at ground — costs one IndexByte.
func (t *inputTok) track(p []byte) {
	if len(t.pend) == 0 && !t.inPaste && bytes.IndexByte(p, ansi.ESC) < 0 &&
		(len(p) == 0 || p[len(p)-1] < utf8.RuneSelf) {
		return
	}
	t.feed(p, false, nil)
}

// reset forgets a carried tail (the focus that was collecting it has gone).
func (t *inputTok) reset() { t.pend = t.pend[:0] }

// feed splits the carried tail plus p into whole tokens, calling emit (if
// non-nil) for each in order; an incomplete last token is carried. With
// loneEsc a read ending in a lone ESC emits it as the Esc key — the router
// cannot hold a key back waiting for a sequence that is not coming. Emitted
// slices are valid only until the next feed.
func (t *inputTok) feed(p []byte, loneEsc bool, emit func(tokKind, []byte)) {
	buf := p
	if len(t.pend) > 0 {
		t.scratch = append(append(t.scratch[:0], t.pend...), p...)
		buf = t.scratch
		t.pend = t.pend[:0]
	}
	for len(buf) > 0 {
		n, k := t.next(buf, loneEsc)
		if n == 0 {
			t.carry(buf, emit)
			return
		}
		if emit != nil {
			emit(k, buf[:n])
		}
		buf = buf[n:]
	}
}

func (t *inputTok) carry(buf []byte, emit func(tokKind, []byte)) {
	if len(buf) <= maxPendingInput {
		t.pend = append(t.pend, buf...)
		return
	}
	t.inPaste = false
	if emit != nil {
		emit(tokKey, buf)
	}
}

// next returns the length and kind of the whole token at the front of buf,
// or 0 when buf holds only the start of one.
func (t *inputTok) next(buf []byte, loneEsc bool) (int, tokKind) {
	if t.inPaste {
		return t.pasteNext(buf)
	}
	seq, _, n, st := ansi.DecodeSequence(buf, ansi.NormalState, nil)
	if st != ansi.NormalState || cutShort(buf, seq, n) {
		if loneEsc && len(buf) == 1 && buf[0] == ansi.ESC {
			return 1, tokKey
		}
		return 0, tokKey
	}
	k, extra := classify(seq)
	n = max(n, 1) + extra
	if len(buf) < n {
		return 0, k
	}
	if k == tokPasteBegin {
		t.inPaste = true
	}
	return n, k
}

// cutShort reports a token the decoder ended at the read's edge where only
// the next read can say whether it is whole: a rune missing bytes (the
// decoder reports a lone lead byte as zero bytes, and folds a truncated rune
// into the grapheme before it), or a string whose ESC \ terminator is split
// (the decoder takes the lone ESC as cancelling it).
func cutShort(buf, seq []byte, n int) bool {
	if buf[0] >= 0xC0 && (n == 0 || n == len(buf)) {
		return endsMidRune(buf)
	}
	return n == len(buf)-1 && buf[n] == ansi.ESC && isStringSeq(seq)
}

// endsMidRune reports whether b ends in the leading bytes of a rune.
func endsMidRune(b []byte) bool {
	for i := len(b) - 1; i >= max(0, len(b)-utf8.UTFMax); i-- {
		if utf8.RuneStart(b[i]) {
			return !utf8.FullRune(b[i:])
		}
	}
	return false
}

// pasteNext emits paste content up to the end marker, holding back a tail
// that could be the start of a split marker.
func (t *inputTok) pasteNext(buf []byte) (int, tokKind) {
	switch i := bytes.Index(buf, pasteEnd); {
	case i == 0:
		t.inPaste = false
		return len(pasteEnd), tokPasteEnd
	case i > 0:
		return i, tokPasteData
	}
	return len(buf) - partialSuffix(buf, pasteEnd), tokPasteData
}

// partialSuffix is the length of the longest suffix of b that is a proper
// prefix of marker.
func partialSuffix(b, marker []byte) int {
	for k := min(len(marker)-1, len(b)); k > 0; k-- {
		if bytes.HasSuffix(b, marker[:k]) {
			return k
		}
	}
	return 0
}

// classify types one complete sequence; extra is how many raw bytes follow
// it as part of the same token (X10 mouse: button, column, row).
func classify(seq []byte) (k tokKind, extra int) {
	switch {
	case ansi.HasCsiPrefix(seq):
		return classifyCSI(seq)
	case isStringSeq(seq):
		return tokReport, 0
	case len(seq) == 2 && seq[0] == ansi.ESC && (seq[1] == 'O' || seq[1] == 'N'):
		return tokKey, 1 // SS3/SS2: the key is the byte after
	}
	return tokKey, 0
}

func isStringSeq(seq []byte) bool {
	return ansi.HasOscPrefix(seq) || ansi.HasDcsPrefix(seq) || ansi.HasApcPrefix(seq) ||
		ansi.HasSosPrefix(seq) || ansi.HasPmPrefix(seq)
}

// reportCSI are the (private marker, final) pairs that only a terminal's
// reply produces.
var reportCSI = map[[2]byte]bool{
	{'?', 'c'}: true, // DA1
	{'>', 'c'}: true, // DA2
	{'=', 'c'}: true, // DA3
	{0, 'n'}:   true, // DSR
	{'?', 'n'}: true, // DEC DSR
	{'?', 'u'}: true, // kitty keyboard flags
}

func classifyCSI(seq []byte) (tokKind, int) {
	body := seq[1:]
	if seq[0] == ansi.ESC {
		body = seq[2:]
	}
	final, params := body[len(body)-1], body[:len(body)-1]
	var marker byte
	if len(params) > 0 && params[0] >= '<' && params[0] <= '?' {
		marker = params[0]
	}
	switch {
	case string(body) == "200~":
		return tokPasteBegin, 0
	case final == 'M' && len(params) == 0:
		return tokMouse, 3 // X10
	case (final == 'M' || final == 'm') && (marker == '<' || marker == 0):
		return tokMouse, 0 // SGR, urxvt
	}
	return csiReplyKind(marker, final, params), 0
}

// csiReplyKind separates a terminal reply from a key. The CPR shape
// `CSI r;c R` is also xterm's modified F3 (`CSI 1;2R`): the collision is the
// protocol's, and a reply the engine is waiting on outranks shift+F3 in a
// modal that binds no function keys.
func csiReplyKind(marker, final byte, params []byte) tokKind {
	switch {
	case reportCSI[[2]byte{marker, final}]:
		return tokReport
	case final == 'R' && bytes.IndexByte(params, ';') >= 0: // CPR, DECXCPR
		return tokReport
	case final == 'y' && bytes.HasSuffix(params, []byte{'$'}): // DECRPM
		return tokReport
	case (final == 'I' || final == 'O') && len(params) == 0: // focus in/out
		return tokReport
	}
	return tokKey
}
