package termemu

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

// keyCase is one key in one keyboard state and the bytes a terminal sends.
type keyCase struct {
	name  string
	state keyState
	key   KeyPress
	want  string
}

var (
	legacy     = keyState{}
	appCursor  = keyState{appCursor: true}
	kittyOn    = keyState{kittyStack: []int{kittyDisambiguate}}
	modifyOne  = keyState{modifyKeys: 1}
	modifyTwo  = keyState{modifyKeys: 2}
	kittyOther = keyState{kittyStack: []int{4}} // flags without disambiguate
)

func press(code rune, mod uv.KeyMod) KeyPress { return KeyPress{Code: code, Mod: mod} }

func typed(code rune, text string, mod uv.KeyMod) KeyPress {
	return KeyPress{Code: code, Text: text, Mod: mod}
}

// TestEncodeKey_EveryEncoding is the encoder's table: each key family in
// each keyboard state the child may have asked for.
func TestEncodeKey_EveryEncoding(t *testing.T) {
	const (
		shift = uv.ModShift
		alt   = uv.ModAlt
		ctrl  = uv.ModCtrl
	)
	cases := []keyCase{
		// CSI n ~ keys, unmodified and with each modifier bit.
		{"insert", legacy, press(uv.KeyInsert, 0), "\x1b[2~"},
		{"delete", legacy, press(uv.KeyDelete, 0), "\x1b[3~"},
		{"pgup", legacy, press(uv.KeyPgUp, 0), "\x1b[5~"},
		{"pgdown", legacy, press(uv.KeyPgDown, 0), "\x1b[6~"},
		{"f5", legacy, press(uv.KeyF5, 0), "\x1b[15~"},
		{"f6", legacy, press(uv.KeyF6, 0), "\x1b[17~"},
		{"f7", legacy, press(uv.KeyF7, 0), "\x1b[18~"},
		{"f8", legacy, press(uv.KeyF8, 0), "\x1b[19~"},
		{"f9", legacy, press(uv.KeyF9, 0), "\x1b[20~"},
		{"f10", legacy, press(uv.KeyF10, 0), "\x1b[21~"},
		{"f11", legacy, press(uv.KeyF11, 0), "\x1b[23~"},
		{"f12", legacy, press(uv.KeyF12, 0), "\x1b[24~"},
		{"shift+delete", legacy, press(uv.KeyDelete, shift), "\x1b[3;2~"},
		{"alt+pgdown", legacy, press(uv.KeyPgDown, alt), "\x1b[6;3~"},
		{"ctrl+f5", legacy, press(uv.KeyF5, ctrl), "\x1b[15;5~"},
		{"super+f12", legacy, press(uv.KeyF12, uv.ModSuper), "\x1b[24;9~"},
		{"hyper+insert", legacy, press(uv.KeyInsert, uv.ModHyper), "\x1b[2;17~"},
		{"meta+f9", legacy, press(uv.KeyF9, uv.ModMeta), "\x1b[20;33~"},
		{"ctrl+alt+shift+f7", legacy, press(uv.KeyF7, ctrl|alt|shift), "\x1b[18;8~"},
		{"caps lock is state, not a modifier", legacy, press(uv.KeyDelete, uv.ModCapsLock|uv.ModNumLock|uv.ModScrollLock), "\x1b[3~"},

		// CSI/SS3 letter keys.
		{"up", legacy, press(uv.KeyUp, 0), "\x1b[A"},
		{"down", legacy, press(uv.KeyDown, 0), "\x1b[B"},
		{"right", legacy, press(uv.KeyRight, 0), "\x1b[C"},
		{"left", legacy, press(uv.KeyLeft, 0), "\x1b[D"},
		{"home", legacy, press(uv.KeyHome, 0), "\x1b[H"},
		{"end", legacy, press(uv.KeyEnd, 0), "\x1b[F"},
		{"f1", legacy, press(uv.KeyF1, 0), "\x1bOP"},
		{"f2", legacy, press(uv.KeyF2, 0), "\x1bOQ"},
		{"f3", legacy, press(uv.KeyF3, 0), "\x1bOR"},
		{"f4", legacy, press(uv.KeyF4, 0), "\x1bOS"},
		{"app cursor up", appCursor, press(uv.KeyUp, 0), "\x1bOA"},
		{"app cursor home", appCursor, press(uv.KeyHome, 0), "\x1bOH"},
		{"shift+up", legacy, press(uv.KeyUp, shift), "\x1b[1;2A"},
		{"ctrl+end", legacy, press(uv.KeyEnd, ctrl), "\x1b[1;5F"},
		{"alt+f1", legacy, press(uv.KeyF1, alt), "\x1b[1;3P"},
		{"app cursor shift+left", appCursor, press(uv.KeyLeft, shift), "\x1b[1;2D"},

		// C0 keys, legacy.
		{"enter", legacy, press(uv.KeyEnter, 0), "\r"},
		{"keypad enter", legacy, press(uv.KeyKpEnter, 0), "\r"},
		{"tab", legacy, press(uv.KeyTab, 0), "\t"},
		{"backspace", legacy, press(uv.KeyBackspace, 0), "\x7f"},
		{"escape", legacy, press(uv.KeyEscape, 0), "\x1b"},
		{"space", legacy, typed(uv.KeySpace, " ", 0), " "},
		{"shift+tab", legacy, press(uv.KeyTab, shift), "\x1b[Z"},
		{"ctrl+space", legacy, press(uv.KeySpace, ctrl), "\x00"},
		{"ctrl+backspace", legacy, press(uv.KeyBackspace, ctrl), "\x08"},
		{"alt+enter", legacy, press(uv.KeyEnter, alt), "\x1b\r"},
		{"shift+enter", legacy, press(uv.KeyEnter, shift), "\x1b\r"},
		{"alt+backspace", legacy, press(uv.KeyBackspace, alt), "\x1b\x7f"},
		{"ctrl+enter falls back to the key", legacy, press(uv.KeyEnter, ctrl), "\r"},
		{"ctrl+shift+tab falls back to the key", legacy, press(uv.KeyTab, ctrl|shift), "\t"},

		// C0 keys under modifyOtherKeys.
		{"mok ctrl+enter", modifyOne, press(uv.KeyEnter, ctrl), "\x1b[27;5;13~"},
		{"mok alt+backspace", modifyOne, press(uv.KeyBackspace, alt), "\x1b[27;3;127~"},
		{"mok shift+tab keeps CSI Z", modifyOne, press(uv.KeyTab, shift), "\x1b[Z"},
		{"mok ctrl+space keeps NUL", modifyOne, press(uv.KeySpace, ctrl), "\x00"},
		{"mok enter unmodified", modifyTwo, press(uv.KeyEnter, 0), "\r"},

		// C0 keys under kitty disambiguate.
		{"kitty shift+enter", kittyOn, press(uv.KeyEnter, shift), "\x1b[13;2u"},
		{"kitty ctrl+tab", kittyOn, press(uv.KeyTab, ctrl), "\x1b[9;5u"},
		{"kitty alt+backspace", kittyOn, press(uv.KeyBackspace, alt), "\x1b[127;3u"},
		{"kitty ctrl+space", kittyOn, press(uv.KeySpace, ctrl), "\x1b[32;5u"},
		{"kitty esc", kittyOn, press(uv.KeyEscape, 0), "\x1b[27u"},
		{"kitty enter unmodified", kittyOn, press(uv.KeyEnter, 0), "\r"},
		{"kitty shift+escape", kittyOn, press(uv.KeyEscape, shift), "\x1b[27;2u"},
		{"kitty flags without disambiguate are legacy", kittyOther, press(uv.KeyEscape, 0), "\x1b"},

		// Text.
		{"rune", legacy, typed('a', "a", 0), "a"},
		{"rune with no text", legacy, press('q', 0), "q"},
		{"multibyte text", legacy, typed('é', "é", 0), "é"},
		{"multibyte rune with no text", legacy, press('é', 0), "é"},
		{"shifted with text", legacy, typed('a', "A", shift), "A"},
		{"shifted, the shifted code", legacy, KeyPress{Code: 'a', ShiftedCode: 'A', Mod: shift}, "A"},
		{"shifted, no shifted code", legacy, press('a', shift), "a"},
		{"unshifted ignores the shifted code", legacy, KeyPress{Code: 'a', ShiftedCode: 'A'}, "a"},
		{"tilde", legacy, typed('~', "~", 0), "~"},
		{"text on a special key", legacy, KeyPress{Code: uv.KeyExtended, Text: "👍"}, "👍"},

		// Ctrl and Alt on text, legacy.
		{"ctrl+a", legacy, press('a', ctrl), "\x01"},
		{"ctrl+z", legacy, press('z', ctrl), "\x1a"},
		{"ctrl+@ has no legacy byte", legacy, press('@', ctrl), ""},
		{"ctrl+[", legacy, press('[', ctrl), "\x1b"},
		{"ctrl+\\", legacy, press('\\', ctrl), "\x1c"},
		{"ctrl+]", legacy, press(']', ctrl), "\x1d"},
		{"ctrl+^", legacy, press('^', ctrl), "\x1e"},
		{"ctrl+_", legacy, press('_', ctrl), "\x1f"},
		{"ctrl+?", legacy, press('?', ctrl), "\x7f"},
		{"ctrl+A", legacy, press('A', ctrl), "\x01"},
		{"ctrl+1 has none", legacy, press('1', ctrl), ""},
		{"ctrl+` has none", legacy, press('`', ctrl), ""},
		{"ctrl+{ has none", legacy, press('{', ctrl), ""},
		{"alt+b", legacy, typed('b', "b", alt), "\x1bb"},
		{"alt+shift+b", legacy, typed('b', "B", alt|shift), "\x1bB"},
		{"ctrl+alt+a", legacy, press('a', ctrl|alt), "\x1b\x01"},
		{"ctrl+alt+1 has none", legacy, press('1', ctrl|alt), ""},
		{"super+a has none", legacy, press('a', uv.ModSuper), ""},

		// Under modifyOtherKeys 2, what legacy cannot say.
		{"mok2 ctrl+1", modifyTwo, press('1', ctrl), "\x1b[27;5;49~"},
		{"mok2 ctrl+alt+1", modifyTwo, press('1', ctrl|alt), "\x1b[27;7;49~"},
		{"mok2 super+a", modifyTwo, press('a', uv.ModSuper), "\x1b[27;9;97~"},
		{"mok2 ctrl+a stays C0", modifyTwo, press('a', ctrl), "\x01"},
		{"mok1 ctrl+1 has none", modifyOne, press('1', ctrl), ""},

		// Under kitty disambiguate.
		{"kitty ctrl+a", kittyOn, press('a', ctrl), "\x1b[97;5u"},
		{"kitty ctrl+alt+a", kittyOn, press('a', ctrl|alt), "\x1b[97;7u"},
		{"kitty super+a", kittyOn, press('a', uv.ModSuper), "\x1b[97;9u"},
		{"kitty alt+a stays ESC a", kittyOn, typed('a', "a", alt), "\x1ba"},
		{"kitty ctrl+shift+a", kittyOn, press('a', ctrl|shift), "\x1b[97;6u"},
		{"kitty text", kittyOn, typed('x', "x", 0), "x"},

		// Keys with no encoding send nothing, never their code point.
		{"f13", legacy, press(uv.KeyF13, 0), ""},
		{"menu", legacy, press(uv.KeyMenu, 0), ""},
		{"a C1 control", legacy, press(0x85, 0), ""},
		{"a C0 control", legacy, press(0x01, 0), ""},
	}
	for _, c := range cases {
		st := c.state
		assert.Equal(t, c.want, string(st.encodeKey(c.key)), c.name)
	}
}

func TestModParam_EachBit(t *testing.T) {
	for _, c := range []struct {
		mod  uv.KeyMod
		want int
	}{
		{0, 1}, {uv.ModShift, 2}, {uv.ModAlt, 3}, {uv.ModCtrl, 5}, {uv.ModSuper, 9},
		{uv.ModHyper, 17}, {uv.ModMeta, 33},
		{uv.ModShift | uv.ModAlt | uv.ModCtrl | uv.ModSuper | uv.ModHyper | uv.ModMeta, 64},
	} {
		assert.Equal(t, c.want, modParam(c.mod), "%v", c.mod)
	}
}
