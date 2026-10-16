package termui

import (
	"fmt"
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
		{"kitty CSI-u, three mods sub-fields", "\x1b[93;5:1:1u", keyMiss},
		{"kitty CSI-u, non-numeric event", "\x1b[93;5:xu", keyMiss},
		{"kitty CSI-u, non-numeric mods", "\x1b[93;x:1u", keyMiss},
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

// TestPrefixKey_EachByteDerivesItsKey pins newPrefixKey's byte-to-character
// map at every range edge. Each accepted prefix byte must match the kitty and
// modifyOtherKeys spelling of ITS key: ctrl-\ is 0x1c, the first of the
// punctuation range, so an off-by-one there maps it to '|' and the prefix is
// silently unreachable under an enhanced keyboard mode.
func TestPrefixKey_EachByteDerivesItsKey(t *testing.T) {
	for _, c := range []struct {
		b  byte
		ch int
	}{
		{1, 'a'}, {26, 'z'},
		{28, '\\'}, {29, ']'}, {30, '^'}, {31, '_'},
		{127, '?'},
	} {
		k := newPrefixKey(c.b)
		kitty := fmt.Sprintf("\x1b[%d;5u", c.ch)
		xterm := fmt.Sprintf("\x1b[27;5;%d~", c.ch)
		assert.Equal(t, keyPress, k.hit([]byte(kitty)), "byte %#x: kitty %q", c.b, kitty)
		assert.Equal(t, keyPress, k.hit([]byte(xterm)), "byte %#x: modifyOtherKeys %q", c.b, xterm)
	}
}

// TestPrefixKey_MalformedKittyKeyFieldIsNoKey pins kitty's key field: the
// code is required and numeric, and there are at most three sub-fields
// (code:shifted:base). A field that breaks any of that is no key at all, even
// when its shifted alternate names the prefix exactly.
func TestPrefixKey_MalformedKittyKeyFieldIsNoKey(t *testing.T) {
	ctrlBracket := newPrefixKey(0x1d)
	require.Equal(t, keyPress, ctrlBracket.hit([]byte("\x1b[93:93;6u")), "control: ctrl+shift with ] as the shifted key")
	for _, c := range []struct{ name, tok string }{
		{"empty code, shifted key ]", "\x1b[:93;6u"},
		{"non-numeric code, shifted key ]", "\x1b[x:93;6u"},
		{"four key sub-fields", "\x1b[93:93:93:93;6u"},
		{"four key sub-fields, plain ctrl", "\x1b[93:125:93:1;5u"},
		{"non-numeric shifted key, plain ctrl", "\x1b[93:x;5u"},
	} {
		assert.Equal(t, keyMiss, ctrlBracket.hit([]byte(c.tok)), "%s: %q", c.name, c.tok)
	}
}

// TestPrefixKey_EmptyEventIsAPress: kitty's event sub-field defaults to 1
// (press) when empty, as every optional parameter takes its default.
func TestPrefixKey_EmptyEventIsAPress(t *testing.T) {
	assert.Equal(t, keyPress, newPrefixKey(0x1d).hit([]byte("\x1b[93;5:u")))
}

// TestPrefixKey_ShiftMakesEveryLetterAnotherKey pins the letter range at both
// ends: ctrl+shift+a and ctrl+shift+z are not ctrl+a and ctrl+z (ParsePrefixKey
// rejects ctrl-z, but chord's letter rule holds over the whole range).
func TestPrefixKey_ShiftMakesEveryLetterAnotherKey(t *testing.T) {
	for _, c := range []struct {
		b    byte
		code int
	}{{1, 'a'}, {26, 'z'}} {
		k := newPrefixKey(c.b)
		assert.Equal(t, keyPress, k.hit(fmt.Appendf(nil, "\x1b[%d;5u", c.code)), "ctrl+%c", c.code)
		assert.Equal(t, keyMiss, k.hit(fmt.Appendf(nil, "\x1b[%d;6u", c.code)), "ctrl+shift+%c is another key", c.code)
	}
}

// TestPrefixKey_ParameterDigits pins csiNum's digit rule through the matcher:
// every digit counts ('0' included: ctrl+f is code 102), leading zeros are
// harmless up to seven digits and an eighth makes the parameter unusable, and
// a non-digit is never folded into the number ("8=" is not 8*10+('='-'0') = 93).
func TestPrefixKey_ParameterDigits(t *testing.T) {
	ctrlF, ctrlBracket := newPrefixKey(6), newPrefixKey(0x1d)
	assert.Equal(t, keyPress, ctrlF.hit([]byte("\x1b[102;5u")), "kitty ctrl+f")
	assert.Equal(t, keyPress, ctrlF.hit([]byte("\x1b[27;5;102~")), "modifyOtherKeys ctrl+f")
	assert.Equal(t, keyPress, ctrlBracket.hit([]byte("\x1b[0000093;5u")), "seven digits")
	assert.Equal(t, keyMiss, ctrlBracket.hit([]byte("\x1b[00000093;5u")), "eight digits")
	assert.Equal(t, keyMiss, ctrlBracket.hit([]byte("\x1b[8=;5u")), "kitty, a non-digit in the code")
	assert.Equal(t, keyMiss, ctrlBracket.hit([]byte("\x1b[27;5;8=~")), "modifyOtherKeys, a non-digit in the code")
}

// TestCsiNum_EmptyTakesTheDefault: an empty parameter is def, usable unless
// def is negative (the parameter is required).
func TestCsiNum_EmptyTakesTheDefault(t *testing.T) {
	for _, c := range []struct {
		def  int
		ok   bool
		name string
	}{{1, true, "optional, default 1"}, {0, true, "optional, default 0"}, {-1, false, "required"}} {
		n, ok := csiNum(nil, c.def)
		assert.Equal(t, c.def, n, c.name)
		assert.Equal(t, c.ok, ok, c.name)
	}
}
