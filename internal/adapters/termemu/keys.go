package termemu

import (
	"strconv"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
)

// KeyPress is one key press, as bubbletea v2 decodes it (tea.KeyPressMsg
// converts to it directly: uv.KeyPressEvent(tea.Key(msg))).
type KeyPress = uv.KeyPressEvent

// MouseEvent is one mouse event in pane-relative cells (tea.MouseMsg values
// convert to the matching uv event types).
type MouseEvent = uv.MouseEvent

// keyState is what the child has asked of its terminal's keyboard: the kitty
// keyboard protocol flags (CSI > flags u and friends), xterm's
// modifyOtherKeys level (CSI > 4 ; n m) and DECCKM (application cursor keys).
//
// x/vt keeps none of the first two and its own SendKey drops every modified
// key it has no table entry for (x#961, Shift+Enter included), so the
// adapter owns the encoder outright. libghostty-vt ships a key encoder that
// would replace this file.
type keyState struct {
	kittyStack []int // top is the active flags; empty means 0
	modifyKeys int
	appCursor  bool
}

func (k *keyState) kittyFlags() int {
	if len(k.kittyStack) == 0 {
		return 0
	}
	return k.kittyStack[len(k.kittyStack)-1]
}

// kittyDisambiguate is kitty's flag 1, the only one claude asks for: escape
// codes for keys that are ambiguous in the legacy encoding.
const kittyDisambiguate = 1

// modParam is the xterm/kitty modifier parameter: 1 + a bit per modifier.
func modParam(m uv.KeyMod) int {
	p := 0
	if m.Contains(uv.ModShift) {
		p |= 1
	}
	if m.Contains(uv.ModAlt) {
		p |= 2
	}
	if m.Contains(uv.ModCtrl) {
		p |= 4
	}
	if m.Contains(uv.ModSuper) {
		p |= 8
	}
	if m.Contains(uv.ModHyper) {
		p |= 16
	}
	if m.Contains(uv.ModMeta) {
		p |= 32
	}
	return p + 1
}

// csiTilde are the keys xterm encodes as CSI n ~.
var csiTilde = map[rune]int{
	uv.KeyInsert: 2, uv.KeyDelete: 3, uv.KeyPgUp: 5, uv.KeyPgDown: 6,
	uv.KeyF5: 15, uv.KeyF6: 17, uv.KeyF7: 18, uv.KeyF8: 19,
	uv.KeyF9: 20, uv.KeyF10: 21, uv.KeyF11: 23, uv.KeyF12: 24,
}

// csiLetter are the keys xterm encodes as CSI 1 ; m X (or SS3 X unmodified).
var csiLetter = map[rune]byte{
	uv.KeyUp: 'A', uv.KeyDown: 'B', uv.KeyRight: 'C', uv.KeyLeft: 'D',
	uv.KeyHome: 'H', uv.KeyEnd: 'F',
	uv.KeyF1: 'P', uv.KeyF2: 'Q', uv.KeyF3: 'R', uv.KeyF4: 'S',
}

// encodeKey returns the bytes a terminal in state k sends for key, or nil
// for a key it has no encoding for.
func (k *keyState) encodeKey(key KeyPress) []byte {
	mods := key.Mod &^ (uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock)
	mp := modParam(mods)

	if n, ok := csiTilde[key.Code]; ok {
		if mp == 1 {
			return []byte("\x1b[" + strconv.Itoa(n) + "~")
		}
		return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(mp) + "~")
	}
	if c, ok := csiLetter[key.Code]; ok {
		switch {
		case mp != 1:
			return []byte("\x1b[1;" + strconv.Itoa(mp) + string(c))
		case c >= 'P' || k.appCursor:
			return []byte{0x1b, 'O', c}
		default:
			return []byte{0x1b, '[', c}
		}
	}

	// Keys with a C0 legacy form: Enter, Tab, Backspace, Escape, Space and
	// the Ctrl+letter range. The keypad's Enter is Enter: the child never
	// asked for an application keypad.
	code := key.Code
	if code == uv.KeyKpEnter {
		code = uv.KeyEnter
	}
	kitty := k.kittyFlags()&kittyDisambiguate != 0
	switch code {
	case uv.KeyEnter, uv.KeyTab, uv.KeyBackspace, uv.KeyEscape, uv.KeySpace:
		legacy := map[rune]byte{uv.KeyEnter: '\r', uv.KeyTab: '\t', uv.KeyBackspace: 0x7f, uv.KeyEscape: 0x1b, uv.KeySpace: ' '}[code]
		switch {
		case mods == 0 && code == uv.KeyEscape && kitty:
			// Disambiguate makes a bare Esc unambiguous: CSI 27 u.
			return []byte("\x1b[27u")
		case mods == 0:
			return []byte{legacy}
		case kitty:
			return []byte("\x1b[" + strconv.Itoa(int(legacy)) + ";" + strconv.Itoa(mp) + "u")
		case code == uv.KeyTab && mods == uv.ModShift:
			return []byte("\x1b[Z")
		case code == uv.KeySpace && mods == uv.ModCtrl:
			return []byte{0}
		case code == uv.KeyBackspace && mods == uv.ModCtrl:
			return []byte{0x08}
		case k.modifyKeys >= 1:
			// xterm modifyOtherKeys form: CSI 27 ; m ; code ~.
			return []byte("\x1b[27;" + strconv.Itoa(mp) + ";" + strconv.Itoa(int(legacy)) + "~")
		case mods == uv.ModAlt || mods == uv.ModShift:
			// The terminal-setup convention (iTerm2's and claude's own
			// /terminal-setup): Shift+Enter and Alt+Enter send ESC CR, which
			// line editors read as "insert a newline".
			return []byte{0x1b, legacy}
		default:
			return []byte{legacy}
		}
	}

	// Text: a printable rune, or a key carrying the text it typed. A special
	// key (above unicode.MaxRune) with no text and no encoding above has
	// none at all — never its code point.
	if code >= 0x20 && code < 0x7f || code > 0x9f && code <= unicode.MaxRune || key.Text != "" {
		text := key.Text
		if text == "" {
			text = string(code)
			if mods.Contains(uv.ModShift) && key.ShiftedCode != 0 {
				text = string(key.ShiftedCode)
			}
		}
		rest := mods &^ uv.ModShift
		switch {
		case rest == 0:
			return []byte(text)
		case kitty && rest != uv.ModAlt:
			return []byte("\x1b[" + strconv.Itoa(int(code)) + ";" + strconv.Itoa(mp) + "u")
		case rest == uv.ModCtrl && ctrlByte(code) != 0:
			return []byte{ctrlByte(code)}
		case rest == uv.ModAlt:
			return append([]byte{0x1b}, text...)
		case rest == uv.ModCtrl|uv.ModAlt && ctrlByte(code) != 0:
			return []byte{0x1b, ctrlByte(code)}
		case k.modifyKeys >= 2:
			return []byte("\x1b[27;" + strconv.Itoa(mp) + ";" + strconv.Itoa(int(code)) + "~")
		}
	}
	return nil
}

// ctrlByte is the C0 byte Ctrl+r produces in the legacy encoding, or 0.
func ctrlByte(r rune) byte {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r-'a') + 1
	case r >= '@' && r <= '_':
		return byte(r - '@')
	case r == '?':
		return 0x7f
	}
	return 0
}
