package termui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPrefixKey_MalformedSpellingsOfThePrefixAreNoKey: each token below is an
// encoding of ctrl+] broken in exactly one place — a field too many, a key
// sub-field that is not a number, a modifier field with an extra sub-field, a
// parameter past seven digits, a modifyOtherKeys lead other than 27. None is the prefix: a near-miss reaches
// the engine as the key it is rather than opening the viewer.
func TestPrefixKey_MalformedSpellingsOfThePrefixAreNoKey(t *testing.T) {
	ctrlBracket := newPrefixKey(0x1d)
	for _, c := range []struct{ name, tok string }{
		{"kitty, four fields", "\x1b[93;5;29;1u"},
		{"kitty, a one-byte non-numeric shifted key", "\x1b[93:x;5u"},
		{"kitty, three modifier sub-fields", "\x1b[93;5:1:1u"},
		{"kitty, an eight-digit shifted key", "\x1b[93:00000125;5u"},
		{"modifyOtherKeys, lead 28", "\x1b[28;5;93~"},
	} {
		assert.Equal(t, keyMiss, ctrlBracket.hit([]byte(c.tok)), "%s: %q", c.name, c.tok)
	}
}
