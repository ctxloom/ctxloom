package termui

import (
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
		// U141-F08: keys the engine needs even more urgently than TAB — the
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
