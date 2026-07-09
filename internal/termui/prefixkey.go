package termui

import (
	"fmt"
	"strings"
)

// ParsePrefixKey maps a config-syntax key name ("ctrl-]", "ctrl+t") to the
// raw control byte the interceptor scans for. Only control keys are accepted:
// a printable prefix would swallow ordinary typing. Keys the engine needs
// verbatim to function at all — escape (ctrl-[), tab (ctrl-i), and the
// newline/return pair (ctrl-j/ctrl-m) — are rejected with the reason.
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
	case 9:
		return 0, fmt.Errorf("prefix key %q: ctrl-i is TAB — intercepting it would break completion", s)
	case 10, 13:
		return 0, fmt.Errorf("prefix key %q: ctrl-j/ctrl-m are newline/return — intercepting them would break typing", s)
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
