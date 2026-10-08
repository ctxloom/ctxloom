package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLabelBinary_ReadsTheNeutralKey: a label's binary is read untyped from
// its body's binary_path, the one key every engine whose label names a
// binary declares it under; anything absent or not a string names none.
func TestLabelBinary_ReadsTheNeutralKey(t *testing.T) {
	for name, tc := range map[string]struct {
		body map[string]any
		want string
	}{
		"set":       {map[string]any{"binary_path": "/opt/claude/bin/claude"}, "/opt/claude/bin/claude"},
		"bare name": {map[string]any{"binary_path": "claude-next"}, "claude-next"},
		"absent":    {map[string]any{"model": "x"}, ""},
		"nil body":  {nil, ""},
		"not text":  {map[string]any{"binary_path": 7}, ""},
		"other key": {map[string]any{"binary": "/opt/x"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, LabelBinary(tc.body))
		})
	}
}
