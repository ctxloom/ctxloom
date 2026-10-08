package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLabelBinary_ReadsTheNeutralKey: a label's binary is read untyped from
// its body's binary_path, the one key every engine whose label names a
// binary declares it under; absent or null names none, and a value that is
// not a string is ErrMalformedLabelValue.
func TestLabelBinary_ReadsTheNeutralKey(t *testing.T) {
	for name, tc := range map[string]struct {
		body map[string]any
		want string
	}{
		"set":       {map[string]any{"binary_path": "/opt/claude/bin/claude"}, "/opt/claude/bin/claude"},
		"bare name": {map[string]any{"binary_path": "claude-next"}, "claude-next"},
		"absent":    {map[string]any{"model": "x"}, ""},
		"nil body":  {nil, ""},
		"null":      {map[string]any{"binary_path": nil}, ""},
		"other key": {map[string]any{"binary": "/opt/x"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LabelBinary(tc.body)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	_, err := LabelBinary(map[string]any{"binary_path": 7})
	require.ErrorIs(t, err, ErrMalformedLabelValue, "a binary_path that is not a string is refused, not read as absent")
	require.ErrorContains(t, err, "binary_path")
}
