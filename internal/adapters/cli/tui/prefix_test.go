package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/termoracle"
)

func TestPrefixKey_ParseRefusesWhatTheTerminalLayerRefuses(t *testing.T) {
	for _, in := range []string{"", "a", "ctrl-c", "ctrl-[", "ctrl-m", "alt-x"} {
		_, err := ParsePrefixKey(in)
		assert.Error(t, err, in)
	}
	k, err := ParsePrefixKey("ctrl+t")
	require.NoError(t, err)
	assert.Equal(t, "^T", k.Caret())
	assert.True(t, k.Matches(keyFromBytes(t, "\x14")))
	assert.False(t, k.Matches(keyFromBytes(t, "\x1d")))
	assert.False(t, PrefixKey{}.Matches(tea.KeyPressMsg{}), "no prefix matches nothing, not even the empty key")
	assert.Empty(t, PrefixKey{}.Caret())
}

func keyFromBytes(t *testing.T, b string) tea.KeyPressMsg {
	t.Helper()
	msgs := decodeInput(b)
	require.Len(t, msgs, 1)
	k, ok := msgs[0].(tea.KeyPressMsg)
	require.True(t, ok)
	return k
}

// TestSplitOracle_PrefixKeyTable: every spelling of ctrl+] the old layer
// matched, bubbletea decodes to a key PrefixKey matches; nothing it refused
// does, but for the malformed spellings bubbletea itself reads as ctrl+]
// (CtrlBracketLenient). A release is a release, never a second press.
func TestSplitOracle_PrefixKeyTable(t *testing.T) {
	k, err := ParsePrefixKey("ctrl-]")
	require.NoError(t, err)
	for _, c := range termoracle.CtrlBracket {
		assert.Equal(t, c.Want, prefixHit(k, c.Bytes), "%s: %q", c.Name, c.Bytes)
	}
	for _, c := range termoracle.CtrlBracketLenient {
		assert.Equal(t, termoracle.Press, prefixHit(k, c.Bytes), "%s: %q", c.Name, c.Bytes)
	}
}

func prefixHit(k PrefixKey, b string) termoracle.Hit {
	msgs := decodeInput(b)
	if len(msgs) != 1 {
		return termoracle.Miss
	}
	switch m := msgs[0].(type) {
	case tea.KeyPressMsg:
		if k.Matches(m) {
			return termoracle.Press
		}
	case tea.KeyReleaseMsg:
		if k.Matches(tea.KeyPressMsg(m)) {
			return termoracle.Release
		}
	}
	return termoracle.Miss
}

// decodeInput turns terminal input bytes into the messages bubbletea would
// deliver for them.
func decodeInput(b string) []tea.Msg {
	var dec uv.EventDecoder
	buf := []byte(b)
	var out []tea.Msg
	for len(buf) > 0 {
		n, ev := dec.Decode(buf)
		if n == 0 {
			break
		}
		buf = buf[n:]
		switch ev := ev.(type) {
		case uv.KeyPressEvent:
			out = append(out, tea.KeyPressMsg(ev))
		case uv.KeyReleaseEvent:
			out = append(out, tea.KeyReleaseMsg(ev))
		case uv.MouseClickEvent:
			out = append(out, tea.MouseClickMsg(ev))
		case uv.MouseReleaseEvent:
			out = append(out, tea.MouseReleaseMsg(ev))
		case uv.MouseWheelEvent:
			out = append(out, tea.MouseWheelMsg(ev))
		case uv.MouseMotionEvent:
			out = append(out, tea.MouseMotionMsg(ev))
		case uv.FocusEvent:
			out = append(out, tea.FocusMsg{})
		case uv.BlurEvent:
			out = append(out, tea.BlurMsg{})
		}
	}
	return out
}

func TestPrefixKey_NoPrefixMatchesNoKeystroke(t *testing.T) {
	assert.False(t, PrefixKey{}.Matches(tea.KeyPressMsg{Code: tea.KeyExtended}), "an empty keystroke is not an empty prefix")
}

func TestParsePrefixKey_Accepted(t *testing.T) {
	for _, c := range []struct {
		in    string
		want  byte
		caret string
	}{
		{"ctrl-]", 0x1d, "^]"},
		{"ctrl+]", 0x1d, "^]"},
		{"Ctrl-]", 0x1d, "^]"},
		{"ctrl-a", 1, "^A"},
		{"ctrl-t", 20, "^T"},
		{"ctrl-\\", 28, "^\\"},
		{"ctrl-^", 30, "^^"},
		{"ctrl-_", 31, "^_"},
		{"ctrl-?", 127, "^?"},
	} {
		got, err := ParsePrefixKey(c.in)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.want, got.b, c.in)
		assert.Equal(t, c.caret, got.Caret(), c.in)
	}
}

// TestParsePrefixKey_Rejected: each refusal names why — not a control key
// at all, a character no control key carries, or a control key the engine
// needs verbatim.
func TestParsePrefixKey_Rejected(t *testing.T) {
	for _, c := range []struct {
		in   string
		want error
	}{
		{"", errPrefixNotControlKey},        // empty
		{"]", errPrefixNotControlKey},       // bare printable
		{"a", errPrefixNotControlKey},       // printable would swallow typing
		{"alt-x", errPrefixNotControlKey},   // unknown modifier
		{"ctrl-", errPrefixNotControlKey},   // no key
		{"ctrl-xy", errPrefixNotControlKey}, // multi-char
		{"ctrl-1", errPrefixNotControlChar}, // below the letters
		{"ctrl-`", errPrefixNotControlChar}, // between _ and a
		{"ctrl-{", errPrefixNotControlChar}, // just past z
		{"ctrl-~", errPrefixNotControlChar}, // past z, where ctrl-a's arithmetic would land on ^^
		{"ctrl-[", errPrefixReserved},       // ESC: shadows every escape sequence
		{"ctrl-i", errPrefixReserved},       // TAB
		{"ctrl-j", errPrefixReserved},       // LF
		{"ctrl-m", errPrefixReserved},       // CR
		{"ctrl-@", errPrefixReserved},       // NUL
		{"ctrl- ", errPrefixNotControlKey},  // the space is trimmed: no key
		{"ctrl-c", errPrefixReserved},       // SIGINT
		{"ctrl-d", errPrefixReserved},       // EOF
		{"ctrl-s", errPrefixReserved},       // XOFF flow control
		{"ctrl-q", errPrefixReserved},       // XON flow control
		{"ctrl-z", errPrefixReserved},       // SIGTSTP
	} {
		_, err := ParsePrefixKey(c.in)
		assert.ErrorIs(t, err, c.want, "%q", c.in)
	}
}
