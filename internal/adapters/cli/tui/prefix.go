package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/ctxloom/ctxloom/internal/adapters/termui"
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

// ParsePrefixKey parses a config-syntax key name ("ctrl-]", "ctrl+t") under
// the same rules the terminal layer has always applied: a control key, and
// none the engine needs verbatim.
func ParsePrefixKey(s string) (PrefixKey, error) {
	b, err := termui.ParsePrefixKey(s)
	if err != nil {
		return PrefixKey{}, err
	}
	return PrefixKey{b: b, keystroke: teaKeyName(b)}, nil
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
	return termui.CaretHint(k.b)
}
