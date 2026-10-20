// Package termoracle is the root terminal surface's behavioural oracle: the
// input tables and the summon contract that the interactive run's terminal
// layer must keep, stated once and run against every implementation of it.
//
// The tables were taken from what the homegrown terminal layer the split view
// replaced did; the split view (internal/adapters/cli/tui, Split) runs them,
// so a behaviour the replacement drops fails a named case instead of going
// unnoticed. Where the
// replacement differs on purpose, the case says so in its own field, never by
// being left out.
package termoracle

// Hit is what one input token is to the prefix key.
type Hit int

const (
	// Miss is any token that is not the prefix: it reaches whatever has focus
	// as the key it is.
	Miss Hit = iota
	// Press is the prefix pressed (or auto-repeated).
	Press
	// Release is the prefix released, which a terminal reports only under
	// kitty's event-type flag: it must never count as a second press.
	Release
)

// PrefixCase is one spelling of a key and what it is to the ctrl+] prefix.
type PrefixCase struct {
	Name  string
	Bytes string
	Want  Hit
}

// CtrlBracket is every spelling of ctrl+] a terminal sends (legacy C0, kitty
// CSI-u with its optional fields, xterm modifyOtherKeys), the near misses
// that must stay ordinary keys, and the malformed spellings that must never
// open the viewer. The engine picks the terminal's keyboard mode (claude-code
// pushes kitty flags and sets modifyOtherKeys), so the prefix is a KEY, not a
// byte: every encoding of it matches, and nothing merely similar does.
var CtrlBracket = []PrefixCase{
	{"legacy C0 byte", "\x1d", Press},
	{"kitty CSI-u", "\x1b[93;5u", Press},
	{"kitty CSI-u, explicit press event", "\x1b[93;5:1u", Press},
	{"kitty CSI-u, repeat event", "\x1b[93;5:2u", Press},
	{"kitty CSI-u, release event", "\x1b[93;5:3u", Release},
	{"kitty CSI-u, alternate shifted key", "\x1b[93:125;5u", Press},
	{"kitty CSI-u, alternate base-layout key only", "\x1b[93::93;5u", Press},
	{"kitty CSI-u, caps lock on", "\x1b[93;69u", Press},
	{"kitty CSI-u, num lock on", "\x1b[93;133u", Press},
	{"kitty CSI-u, associated text field", "\x1b[93;5;29u", Press},
	{"xterm modifyOtherKeys", "\x1b[27;5;93~", Press},
	{"C1 CSI introducer", "\x9b93;5u", Press},

	{"ctrl+[ legacy (ESC)", "\x1b", Miss},
	{"ctrl+[ kitty", "\x1b[91;5u", Miss},
	{"ctrl+\\ kitty", "\x1b[92;5u", Miss},
	{"] with no modifier", "\x1b[93u", Miss},
	{"] with modifier 1 (none)", "\x1b[93;1u", Miss},
	{"alt+]", "\x1b[93;3u", Miss},
	{"ctrl+alt+]", "\x1b[93;7u", Miss},
	{"ctrl+super+]", "\x1b[93;13u", Miss},
	{"modifyOtherKeys alt+]", "\x1b[27;3;93~", Miss},
	{"modifyOtherKeys ctrl+}", "\x1b[27;6;125~", Miss},
	{"modifyOtherKeys short form", "\x1b[27;5~", Miss},
	{"kitty flags reply", "\x1b[?5u", Miss},
	{"F5 (CSI 15~)", "\x1b[15~", Miss},
	{"plain ]", "]", Miss},

	{"malformed: kitty, non-numeric event", "\x1b[93;5:xu", Miss},
	{"malformed: kitty, non-numeric mods", "\x1b[93;x:1u", Miss},
	{"malformed: kitty, a one-byte non-numeric shifted key", "\x1b[93:x;5u", Miss},
	{"malformed: SCORC (CSI u, no params)", "\x1b[u", Miss},
	{"malformed: signed parameter", "\x1b[+93;5u", Miss},
	{"malformed: modifyOtherKeys, lead 28", "\x1b[28;5;93~", Miss},
}

// CtrlBracketLenient are malformed spellings of ctrl+] that the byte-level
// layer refuses and bubbletea's decoder reads as the key they nearly spell.
// A layer that matches decoded keys (the split view) therefore sees ctrl+]
// in each; the engine never receives the malformed bytes either way, since
// the split view re-encodes every key it forwards.
var CtrlBracketLenient = []PrefixCase{
	{"malformed: kitty, unknown event", "\x1b[93;5:4u", Miss},
	{"malformed: kitty, three modifier sub-fields", "\x1b[93;5:1:1u", Miss},
	{"malformed: kitty, four fields", "\x1b[93;5;29;1u", Miss},
	{"malformed: kitty, an eight-digit shifted key", "\x1b[93:00000125;5u", Miss},
}

// TokenKind classifies one whole unit of terminal input.
type TokenKind int

const (
	// Key is anything the human typed: a rune, a control byte, a key's
	// escape sequence.
	Key TokenKind = iota
	// Report is the terminal answering a query (CPR, DA, OSC colour, focus,
	// DECRPM, XTVERSION). The engine asked, so it is the engine's, never a
	// keystroke for whatever holds focus.
	Report
	// Mouse is a mouse report a tracking mode asked for.
	Mouse
	// Paste is one whole bracketed paste.
	Paste
)

// Token is one input token and its kind.
type Token struct {
	Name  string
	Bytes string
	Kind  TokenKind
}

// Tokens is the input vocabulary every router of the human's input must keep
// whole: keys (a rune, a control, cursor and function keys, alt+key),
// terminal replies, mouse reports, and a bracketed paste carrying an escape
// sequence and a CR. Each lands whole on exactly one side — the engine or the
// surface — and a key is never split from its sequence. A lone ESC is left
// out: followed by anything it IS a different key.
var Tokens = []Token{
	{"rune", "a", Key},
	{"multibyte rune", "é", Key},
	{"return", "\r", Key},
	{"rune y", "y", Key},
	{"digit", "1", Key},
	{"cursor up", "\x1b[A", Key},
	{"F1 (SS3)", "\x1bOP", Key},
	{"alt+a", "\x1ba", Key},
	{"ctrl+right", "\x1b[1;5C", Key},
	{"cursor position report", "\x1b[12;40R", Report},
	{"device attributes reply", "\x1b[?62;22c", Report},
	{"OSC 11 background reply", "\x1b]11;rgb:1/2/3\x1b\\", Report},
	{"focus in", "\x1b[I", Report},
	{"DECRPM reply", "\x1b[?2026;2$y", Report},
	{"XTVERSION reply", "\x1bP>|xterm(390)\x1b\\", Report},
	{"SGR mouse press", "\x1b[<0;1;2M", Mouse},
	{"X10 mouse press", "\x1b[M !!", Mouse},
	{"bracketed paste", "\x1b[200~ab\r\x1b[31mc\x1b[201~", Paste},
}
