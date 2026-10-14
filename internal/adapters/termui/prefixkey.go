package termui

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// ParsePrefixKey maps a config-syntax key name ("ctrl-]", "ctrl+t") to the
// control byte a legacy terminal sends for that key. The byte names the key
// exactly (ctrl plus one character); the interceptor matches the KEY, in
// every encoding a terminal may use for it (see prefixKey). Only control keys are accepted:
// a printable prefix would swallow ordinary typing. Keys the engine needs
// verbatim to function at all — escape (ctrl-[), tab (ctrl-i), the
// newline/return pair (ctrl-j/ctrl-m), interrupt/EOF (ctrl-c/ctrl-d),
// suspend (ctrl-z), and flow control (ctrl-q/ctrl-s) — are rejected with the
// reason.
func ParsePrefixKey(s string) (byte, error) {
	name := strings.ToLower(strings.TrimSpace(s))
	rest, ok := strings.CutPrefix(name, "ctrl-")
	if !ok {
		rest, ok = strings.CutPrefix(name, "ctrl+")
	}
	if !ok || len(rest) != 1 {
		return 0, fmt.Errorf("prefix key %q: want a control key like %q", s, "ctrl-]")
	}
	var b byte
	switch ch := rest[0]; {
	case ch >= 'a' && ch <= 'z':
		b = ch - 'a' + 1
	case ch == '@', ch == ' ':
		b = 0
	case ch >= '[' && ch <= '_': // [ \ ] ^ _
		b = ch - '[' + 27
	case ch == '?':
		b = 127
	default:
		return 0, fmt.Errorf("prefix key %q: %q is not a control-key character", s, string(ch))
	}
	switch b {
	case 0:
		return 0, fmt.Errorf("prefix key %q: NUL is not usable as a prefix", s)
	case 3:
		return 0, fmt.Errorf("prefix key %q: ctrl-c sends SIGINT — intercepting it would swallow every interrupt", s)
	case 4:
		return 0, fmt.Errorf("prefix key %q: ctrl-d is EOF — intercepting it would break input termination", s)
	case 9:
		return 0, fmt.Errorf("prefix key %q: ctrl-i is TAB — intercepting it would break completion", s)
	case 10, 13:
		return 0, fmt.Errorf("prefix key %q: ctrl-j/ctrl-m are newline/return — intercepting them would break typing", s)
	case 17, 19:
		return 0, fmt.Errorf("prefix key %q: ctrl-q/ctrl-s are terminal flow control (XON/XOFF) — intercepting them would break resume/pause", s)
	case 26:
		return 0, fmt.Errorf("prefix key %q: ctrl-z sends SIGTSTP — intercepting it would swallow every suspend", s)
	case 27:
		return 0, fmt.Errorf("prefix key %q: ESC prefixes every terminal escape sequence (and is deliberately not the viewer key)", s)
	}
	return b, nil
}

// CaretHint renders the prefix byte in caret notation ("^]") for the surround
// bar's key hint.
func CaretHint(b byte) string {
	if b == 127 {
		return "^?"
	}
	return "^" + string(rune(b+64))
}

// prefixKey is the prefix as a KEY rather than a byte. A legacy terminal sends
// Ctrl+] as the C0 byte 0x1d; once the engine turns on an enhanced keyboard
// mode — claude-code pushes kitty keyboard flags (CSI >5u) and sets xterm
// modifyOtherKeys 2 (CSI >4;2m) when it detects a capable terminal or tmux —
// the same press arrives as CSI 93;5u (kitty) or CSI 27;5;93~
// (modifyOtherKeys). The engine's mode request passes through to the real
// terminal, so the interceptor must know every spelling.
type prefixKey struct {
	b  byte // the legacy C0 (or DEL) byte
	ch int  // the character held with ctrl: ']' for 0x1d, 't' for 0x14
}

// newPrefixKey derives the key from a byte ParsePrefixKey accepted.
func newPrefixKey(b byte) prefixKey {
	k := prefixKey{b: b, ch: int(b) + 0x60} // 1..26 → a..z
	switch {
	case b >= 28 && b <= 31: // \ ] ^ _
		k.ch = int(b) + 0x40
	case b == 127:
		k.ch = '?'
	}
	return k
}

// keyHit is what one input token is to the prefix.
type keyHit int

const (
	keyMiss keyHit = iota
	keyPress
	// keyRelease is the prefix's release, which a terminal reports only under
	// kitty's event-type flag.
	keyRelease
)

// Modifier bits as kitty and xterm encode them (the wire value is 1 + bits).
const (
	modShift = 1
	modCtrl  = 4
	// modLocks are caps lock and num lock: state, not part of the chord.
	modLocks = 64 | 128
)

// hit classifies one whole input token (an inputTok token, never a fragment).
func (k prefixKey) hit(tok []byte) keyHit {
	if len(tok) == 1 {
		if tok[0] == k.b {
			return keyPress
		}
		return keyMiss
	}
	if !ansi.HasCsiPrefix(tok) {
		return keyMiss
	}
	final, params := csiParts(tok)
	fields := bytes.Split(params, []byte{';'})
	switch final {
	case 'u':
		return k.kitty(fields)
	case '~':
		return k.modifyOtherKeys(fields)
	}
	return keyMiss
}

// kitty matches CSI code[:shifted[:base]] ; mods[:event] [; text] u. The base
// layout key is ignored: the key is the character the layout produces, which
// is what a legacy terminal's C0 byte stands for.
func (k prefixKey) kitty(fields [][]byte) keyHit {
	if len(fields) < 2 || len(fields) > 3 {
		return keyMiss
	}
	keys := bytes.Split(fields[0], []byte{':'})
	code, ok := csiNum(keys[0], -1)
	if !ok || len(keys) > 3 {
		return keyMiss
	}
	shifted := -1
	if len(keys) > 1 {
		if shifted, ok = csiNum(keys[1], -1); !ok && len(keys[1]) > 0 {
			return keyMiss
		}
	}
	modEv := bytes.Split(fields[1], []byte{':'})
	if len(modEv) > 2 {
		return keyMiss
	}
	mods, ok := csiNum(modEv[0], 1)
	if !ok {
		return keyMiss
	}
	event := 1
	if len(modEv) == 2 {
		if event, ok = csiNum(modEv[1], 1); !ok {
			return keyMiss
		}
	}
	if !k.chord(code, shifted, mods) {
		return keyMiss
	}
	switch event {
	case 1, 2: // press, repeat — a held legacy key repeats its byte too
		return keyPress
	case 3:
		return keyRelease
	}
	return keyMiss
}

// modifyOtherKeys matches xterm's CSI 27 ; mods ; code ~, where code is the
// character the key produced (shift already applied).
func (k prefixKey) modifyOtherKeys(fields [][]byte) keyHit {
	if len(fields) != 3 || string(fields[0]) != "27" {
		return keyMiss
	}
	mods, ok1 := csiNum(fields[1], -1)
	code, ok2 := csiNum(fields[2], -1)
	if ok1 && ok2 && k.chord(code, -1, mods) {
		return keyPress
	}
	return keyMiss
}

// chord reports whether code (and kitty's shifted alternate) held with mods is
// ctrl+ch. Shift is part of typing a shifted character (ctrl+_ is
// ctrl+shift+- on a US layout) but makes a letter another key: kitty reports
// ctrl+shift+t as code 't' with shift, which is not ctrl+t.
func (k prefixKey) chord(code, shifted, mods int) bool {
	bits := (mods - 1) &^ modLocks
	switch bits {
	case modCtrl:
		return code == k.ch
	case modCtrl | modShift:
		letter := k.ch >= 'a' && k.ch <= 'z'
		return shifted == k.ch || (code == k.ch && !letter)
	}
	return false
}

// csiNum parses one decimal CSI parameter; empty is def, and a negative def
// marks the parameter required. Anything but digits is not a parameter a key
// encoding uses.
func csiNum(b []byte, def int) (int, bool) {
	if len(b) == 0 {
		return def, def >= 0
	}
	if len(b) > 7 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}
