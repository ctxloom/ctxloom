package tui

import (
	"testing"
	"unicode"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
)

// TestSanitizeForDisplay_Goldens is T14: every character a child could use to
// repaint the modal or hide part of what it asks for — ESC and the sequences
// it opens (CSI, OSC, DCS), the other C0 controls, DEL, C1, bidi overrides,
// zero-width and tag characters, invalid UTF-8 — comes out as a visible
// marker, never as the character itself. Text a human should read unchanged
// stays unchanged.
func TestSanitizeForDisplay_Goldens(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain text is untouched", "rm -rf build/ && make test", "rm -rf build/ && make test"},
		{"a newline is a line break, kept", "a\nb", "a\nb"},
		{"CRLF is one line break", "a\r\nb", "a\nb"},
		{"a lone CR could overwrite the line: visible", "rm -rf /\rls", "rm -rf /⟨U+000D⟩ls"},
		{"a tab expands to spaces", "a\tb", "a    b"},
		{"CSI clear screen", "echo\x1b[2Jhi", "echo⟨ESC⟩[2Jhi"},
		{"CSI cursor home + fake button", "ok\x1b[H[ Allow ]", "ok⟨ESC⟩[H[ Allow ]"},
		{"SGR colour", "\x1b[31mred\x1b[0m", "⟨ESC⟩[31mred⟨ESC⟩[0m"},
		{"OSC title terminated by BEL", "\x1b]0;pwned\x07", "⟨ESC⟩]0;pwned⟨U+0007⟩"},
		{"OSC 52 clipboard write terminated by ST", "\x1b]52;c;cm0gLXJmIC8=\x1b\\", "⟨ESC⟩]52;c;cm0gLXJmIC8=⟨ESC⟩\\"},
		{"DCS", "\x1bP1$r\x1b\\", "⟨ESC⟩P1$r⟨ESC⟩\\"},
		{"backspace erases on a terminal", "safe\b\b\b\bevil", "safe⟨U+0008⟩⟨U+0008⟩⟨U+0008⟩⟨U+0008⟩evil"},
		{"NUL and DEL", "a\x00b\x7fc", "a⟨U+0000⟩b⟨U+007F⟩c"},
		{"C1 CSI (U+009B) acts as ESC [ on some terminals", "a\u009b2Jb", "a⟨U+009B⟩2Jb"},
		{"C1 NEL", "a\u0085b", "a⟨U+0085⟩b"},
		{"bidi override reorders what is shown", "ls \u202egnp.exe", "ls ⟨U+202E⟩gnp.exe"},
		{"bidi isolates", "a\u2066b\u2069c", "a⟨U+2066⟩b⟨U+2069⟩c"},
		{"LRM/RLM/ALM", "\u200e\u200f\u061c", "⟨U+200E⟩⟨U+200F⟩⟨U+061C⟩"},
		{"zero-width space hides an argument boundary", "rm\u200b -rf", "rm⟨U+200B⟩ -rf"},
		{"ZWNJ, ZWJ, word joiner, BOM", "\u200c\u200d\u2060\ufeff", "⟨U+200C⟩⟨U+200D⟩⟨U+2060⟩⟨U+FEFF⟩"},
		{"soft hyphen", "a\u00adb", "a⟨U+00AD⟩b"},
		{"tag characters smuggle invisible text", "hi\U000E0041\U000E0042", "hi⟨U+E0041⟩⟨U+E0042⟩"},
		{"line and paragraph separators", "a\u2028b\u2029c", "a⟨U+2028⟩b⟨U+2029⟩c"},
		{"invalid UTF-8 byte", "a\xffb", "a⟨0xFF⟩b"},
		{"wide and accented text is untouched", "日本語 café", "日本語 café"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeForDisplay(tc.in))
		})
	}
}

// TestSanitizeForDisplay_NothingLiveSurvives is the property the goldens
// sample: for every rune from U+0000 to U+FFFF plus the tag block, the output
// holds no C0 but the newline, no DEL, no C1 and no format character.
func TestSanitizeForDisplay_NothingLiveSurvives(t *testing.T) {
	check := func(r rune) {
		out := sanitizeForDisplay("x" + string(r) + "y")
		for _, o := range out {
			if o == '\n' {
				continue
			}
			// Judged by category here, independently of the implementation.
			if unicode.IsControl(o) || unicode.In(o, unicode.Cf, unicode.Zl, unicode.Zp) {
				t.Fatalf("U+%04X survived sanitizing as U+%04X in %q", r, o, out)
			}
		}
	}
	for r := rune(0); r <= 0xFFFF; r++ {
		if utf16.IsSurrogate(r) {
			continue // not encodable: a Go string cannot carry one (invalid bytes are a golden)
		}
		check(r)
	}
	for r := rune(0xE0000); r <= 0xE007F; r++ {
		check(r)
	}
}
