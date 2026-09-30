package tui

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sanitizeForDisplay makes child-controlled text safe to put on the approval
// modal: every character that could act on the terminal or hide what the
// human is asked to approve becomes a visible marker, and the rest is kept.
//
// A child is untrusted and can put anything in a tool call, a plan or a
// question. Raw, an ESC would let it repaint the modal (draw a fake button,
// clear the command it is asking to run); a CR or backspace would let it
// overwrite part of a line; a bidi override, a zero-width or a tag character
// would let it show one command and run another. So ESC, every other C0
// control but the newline, DEL, C1, invalid UTF-8 and every Unicode format
// character (bidi, zero-width, tag) or line/paragraph separator is shown as
// ⟨ESC⟩, ⟨U+XXXX⟩ or ⟨0xNN⟩. A child can type a marker's text itself, but
// that only makes its request look MORE suspicious; it can never hide one.
//
// A newline is a line break the caller lays out (CRLF counts as one); a tab
// is expanded, since its width is the terminal's and not the layout's.
// Wrapping is the caller's too, and never truncates silently.
func sanitizeForDisplay(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteString("⟨0x")
			b.WriteString(strings.ToUpper(strconv.FormatUint(uint64(s[i]), 16)))
			b.WriteString("⟩")
		case r == '\n':
			b.WriteByte('\n')
		case r == '\t':
			b.WriteString(tabSpaces)
		case r == 0x1b:
			b.WriteString("⟨ESC⟩")
		case !displaySafe(r):
			b.WriteString(runeMarker(r))
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// tabSpaces is what a tab becomes.
const tabSpaces = "    "

// displaySafe reports whether r shows as itself and acts on nothing: not a
// control (C0, DEL, C1), not a format character, not a line or paragraph
// separator. The soft hyphen is a format character, so it is covered.
func displaySafe(r rune) bool {
	return !unicode.IsControl(r) && !unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// runeMarker is r's visible stand-in, "⟨U+XXXX⟩".
func runeMarker(r rune) string {
	hex := strings.ToUpper(strconv.FormatInt(int64(r), 16))
	if len(hex) < 4 {
		hex = strings.Repeat("0", 4-len(hex)) + hex
	}
	return "⟨U+" + hex + "⟩"
}
