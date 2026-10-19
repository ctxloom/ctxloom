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
