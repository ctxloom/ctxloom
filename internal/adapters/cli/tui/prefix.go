package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// PrefixKey is the configured prefix key (ui.prefix_key) as the split view
// matches it: a key bubbletea has already decoded, so every encoding a
// terminal may send for it — the legacy C0 byte, kitty's CSI-u with its lock
// bits and optional fields, xterm's modifyOtherKeys — is the same key, and
// the split view parses no input bytes of its own.
type PrefixKey struct {
	b         byte
	keystroke string // bubbletea's keystroke name for the key, e.g. "ctrl+]"
}

// ParsePrefixKey parses a config-syntax key name ("ctrl-]", "ctrl+t"). Only
// control keys are accepted: a printable prefix would swallow ordinary
// typing. Keys the engine needs verbatim to function at all — escape
// (ctrl-[), tab (ctrl-i), the newline/return pair (ctrl-j/ctrl-m),
// interrupt/EOF (ctrl-c/ctrl-d), suspend (ctrl-z), and flow control
// (ctrl-q/ctrl-s) — are rejected with the reason.
func ParsePrefixKey(s string) (PrefixKey, error) {
	b, err := prefixByte(s)
	if err != nil {
		return PrefixKey{}, err
	}
	return PrefixKey{b: b, keystroke: teaKeyName(b)}, nil
}

// prefixByte maps a config-syntax key name to the control byte a legacy
// terminal sends for that key (DEL for ctrl-?).
func prefixByte(s string) (byte, error) {
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
	if why, refused := refusedPrefixes[b]; refused {
		return 0, fmt.Errorf("prefix key %q: %s", s, why)
	}
	return b, nil
}

// refusedPrefixes are the control bytes the engine needs verbatim, each with
// why it cannot be the prefix.
var refusedPrefixes = map[byte]string{
	0:  "NUL is not usable as a prefix",
	3:  "ctrl-c sends SIGINT — intercepting it would swallow every interrupt",
	4:  "ctrl-d is EOF — intercepting it would break input termination",
	9:  "ctrl-i is TAB — intercepting it would break completion",
	10: "ctrl-j/ctrl-m are newline/return — intercepting them would break typing",
	13: "ctrl-j/ctrl-m are newline/return — intercepting them would break typing",
	17: "ctrl-q/ctrl-s are terminal flow control (XON/XOFF) — intercepting them would break resume/pause",
	19: "ctrl-q/ctrl-s are terminal flow control (XON/XOFF) — intercepting them would break resume/pause",
	26: "ctrl-z sends SIGTSTP — intercepting it would swallow every suspend",
	27: "ESC prefixes every terminal escape sequence (and is deliberately not the viewer key)",
}

// Matches reports whether msg is a press of the prefix. It compares the
// keystroke, not the text a key carries: kitty may attach the key's C0 text,
// and a terminal's lock modifiers are state, not part of the chord.
func (k PrefixKey) Matches(msg tea.KeyPressMsg) bool {
	return k.keystroke != "" && msg.Keystroke() == k.keystroke
}

// Caret is the prefix in caret notation ("^]") for the key hints.
func (k PrefixKey) Caret() string {
	if k.keystroke == "" {
		return ""
	}
	return caretHint(k.b)
}

// caretHint renders a control byte in caret notation.
func caretHint(b byte) string {
	if b == 127 {
		return "^?"
	}
	return "^" + string(rune(b+64))
}

// teaKeyName maps a raw control byte onto bubbletea's key-name vocabulary, so
// the prefix matches the key bubbletea decoded.
func teaKeyName(b byte) string {
	switch {
	case b >= 1 && b <= 26:
		return "ctrl+" + string(rune('a'+b-1))
	case b >= 28 && b <= 31: // \ ] ^ _
		return "ctrl+" + string(rune('['+b-27))
	case b == 127:
		return "backspace"
	default:
		return "ctrl+@"
	}
}
