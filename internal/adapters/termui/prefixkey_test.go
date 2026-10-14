package termui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePrefixKey_Accepted(t *testing.T) {
	for _, c := range []struct {
		in   string
		want byte
	}{
		{"ctrl-]", 0x1d},
		{"ctrl+]", 0x1d},
		{"Ctrl-]", 0x1d},
		{"ctrl-a", 1},
		{"ctrl-t", 20},
		{"ctrl-\\", 28},
		{"ctrl-^", 30},
		{"ctrl-_", 31},
		{"ctrl-?", 127},
	} {
		got, err := ParsePrefixKey(c.in)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got, c.in)
	}
}

func TestParsePrefixKey_Rejected(t *testing.T) {
	for _, in := range []string{
		"",        // empty
		"]",       // bare printable
		"a",       // printable would swallow typing
		"alt-x",   // unknown modifier
		"ctrl-",   // no key
		"ctrl-xy", // multi-char
		"ctrl-[",  // ESC: shadows every escape sequence (and O2 says not ESC)
		"ctrl-i",  // TAB
		"ctrl-j",  // LF
		"ctrl-m",  // CR
		"ctrl-@",  // NUL
		// Keys the engine needs even more urgently than TAB — the
		// old reject list let these through, so e.g. ui.prefix_key: "ctrl-c"
		// passed validation and then swallowed every interrupt.
		"ctrl-c", // SIGINT
		"ctrl-d", // EOF
		"ctrl-s", // XOFF flow control
		"ctrl-q", // XON flow control
		"ctrl-z", // SIGTSTP
	} {
		_, err := ParsePrefixKey(in)
		assert.Error(t, err, "%q must be rejected", in)
	}
}

func TestCaretHint(t *testing.T) {
	assert.Equal(t, "^]", CaretHint(0x1d))
	assert.Equal(t, "^T", CaretHint(20))
	assert.Equal(t, "^?", CaretHint(127))
}

// TestPrefixKey_RecognisesEveryEncodingOfTheKey pins that the prefix is a KEY,
// not a byte. A terminal in an enhanced keyboard mode — which the engine
// turns on: claude-code pushes kitty flags 5 (CSI >5u) and sets
// modifyOtherKeys 2 (CSI >4;2m) whenever it detects a capable terminal or
// tmux — no longer sends the C0 byte for Ctrl+], so a byte-only matcher never
// sees the prefix and the viewer cannot be opened. Every spelling of the
// configured key matches; a key that is merely similar does not.
func TestPrefixKey_RecognisesEveryEncodingOfTheKey(t *testing.T) {
	ctrlBracket := newPrefixKey(0x1d)
	for _, c := range []struct {
		name string
		tok  string
		want keyHit
	}{
		{"legacy C0 byte", "\x1d", keyPress},
		{"kitty CSI-u", "\x1b[93;5u", keyPress},
		{"kitty CSI-u, explicit press event", "\x1b[93;5:1u", keyPress},
		{"kitty CSI-u, repeat event", "\x1b[93;5:2u", keyPress},
		{"kitty CSI-u, release event", "\x1b[93;5:3u", keyRelease},
		{"kitty CSI-u, alternate shifted key", "\x1b[93:125;5u", keyPress},
		{"kitty CSI-u, alternate base-layout key only", "\x1b[93::93;5u", keyPress},
		{"kitty CSI-u, caps lock on", "\x1b[93;69u", keyPress},
		{"kitty CSI-u, num lock on", "\x1b[93;133u", keyPress},
		{"kitty CSI-u, associated text field", "\x1b[93;5;29u", keyPress},
		{"xterm modifyOtherKeys", "\x1b[27;5;93~", keyPress},
		{"C1 CSI introducer", "\x9b93;5u", keyPress},

		{"ctrl+[ legacy (ESC)", "\x1b", keyMiss},
		{"ctrl+[ kitty", "\x1b[91;5u", keyMiss},
		{"ctrl+\\ kitty", "\x1b[92;5u", keyMiss},
		{"] with no modifier", "\x1b[93u", keyMiss},
		{"] with modifier 1 (none)", "\x1b[93;1u", keyMiss},
		{"alt+]", "\x1b[93;3u", keyMiss},
		{"ctrl+alt+]", "\x1b[93;7u", keyMiss},
		{"ctrl+super+]", "\x1b[93;13u", keyMiss},
		{"modifyOtherKeys alt+]", "\x1b[27;3;93~", keyMiss},
		{"modifyOtherKeys ctrl+}", "\x1b[27;6;125~", keyMiss},
		{"modifyOtherKeys short form", "\x1b[27;5~", keyMiss},
		{"kitty flags reply", "\x1b[?5u", keyMiss},
		{"kitty CSI-u, unknown event", "\x1b[93;5:4u", keyMiss},
		{"SCORC (CSI u, no params)", "\x1b[u", keyMiss},
		{"signed parameter", "\x1b[+93;5u", keyMiss},
		{"F5 (CSI 15~)", "\x1b[15~", keyMiss},
		{"plain ]", "]", keyMiss},
	} {
		assert.Equal(t, c.want, ctrlBracket.hit([]byte(c.tok)), "%s: %q", c.name, c.tok)
	}
}

// TestPrefixKey_LettersAndShiftedPunctuation pins the shift rule. A letter
// prefix is ctrl+letter only: kitty sends ctrl+shift+t as code 't' with shift,
// a different key. A punctuation prefix the human types WITH shift (ctrl+_ is
// ctrl+shift+- on a US layout) arrives as the shifted key in kitty's alternate
// field, or as the produced character in modifyOtherKeys.
func TestPrefixKey_LettersAndShiftedPunctuation(t *testing.T) {
	ctrlT, ctrlUnderscore, ctrlQuestion := newPrefixKey(20), newPrefixKey(31), newPrefixKey(127)
	assert.Equal(t, keyPress, ctrlT.hit([]byte("\x1b[116;5u")), "kitty ctrl+t")
	assert.Equal(t, keyPress, ctrlT.hit([]byte("\x1b[27;5;116~")), "modifyOtherKeys ctrl+t")
	assert.Equal(t, keyMiss, ctrlT.hit([]byte("\x1b[116;6u")), "kitty ctrl+shift+t is another key")
	assert.Equal(t, keyMiss, ctrlT.hit([]byte("\x1b[116:84;6u")), "kitty ctrl+shift+t with its shifted alternate")
	assert.Equal(t, keyMiss, ctrlT.hit([]byte("\x1b[27;6;84~")), "modifyOtherKeys ctrl+shift+t")
	assert.Equal(t, keyMiss, ctrlT.hit([]byte("\x1b[84;5u")), "ctrl+T (upper) is not the kitty spelling of ctrl+t")

	assert.Equal(t, keyPress, ctrlUnderscore.hit([]byte("\x1b[45:95;6u")), "kitty ctrl+shift+- reporting _")
	assert.Equal(t, keyPress, ctrlUnderscore.hit([]byte("\x1b[27;6;95~")), "modifyOtherKeys ctrl+_")
	assert.Equal(t, keyMiss, ctrlUnderscore.hit([]byte("\x1b[45;5u")), "ctrl+- is another key")
	assert.Equal(t, keyPress, ctrlQuestion.hit([]byte("\x1b[47:63;6u")), "kitty ctrl+shift+/ reporting ?")
	assert.Equal(t, keyPress, ctrlQuestion.hit([]byte{127}), "legacy DEL")
}
