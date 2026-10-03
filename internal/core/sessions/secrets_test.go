package sessions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every value that CAN round-trip comes back byte for byte, and reading
// expands nothing: a $NAME in a value is the value, whatever the environment
// or the file's other keys hold.
func TestSecrets_RoundTripHostileValues(t *testing.T) {
	t.Setenv("HOME", "/should/not/appear")
	in := map[string]string{
		"DOLLAR":    "$HOME and ${HOME} and $(HOME)",
		"HOME":      "a key the dollar values must not expand to",
		"EQUALS":    "a=b==c",
		"NEWLINES":  "line1\nline2\r\nline3",
		"UNICODE":   "snow ☃ — 日本語",
		"SINGLE":    "it's 'quoted'",
		"INNER_DQ":  `say "hi" then`,
		"LEAD_DQ":   `"starts quoted`,
		"BACKSLASH": `C:\path\to\n not a newline`,
		"SHELL":     "`cmd` !bang #hash",
		"SPACES":    "  padded  ",
		"EMPTY":     "",
		"TOKEN":     "sk-ant-oat01-AbC_123-xyz",
		"BIGNUM":    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	b, err := EncodeSecrets(in)
	require.NoError(t, err)
	out, err := DecodeSecrets(b)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

// The format's known failures are refused at write time, naming the variable
// and never the value, rather than written corrupt.
func TestSecrets_WriterRefusesWhatCannotRoundTrip(t *testing.T) {
	for name, v := range map[string]string{
		"trailing backslash":          `ends in \`,
		"trailing double quote":       `ends quoted"`,
		"integer with a leading zero": "007",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := EncodeSecrets(map[string]string{"OK": "fine", "BAD": v})
			require.ErrorIs(t, err, ErrSecretNotRoundTrippable)
			assert.Contains(t, err.Error(), "BAD")
			assert.NotContains(t, err.Error(), v, "the error never carries the secret")
		})
	}
}
