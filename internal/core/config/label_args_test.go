package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLabelArgs_ReadsTheNeutralKey: a label's args are read untyped from its
// body's args, the one key every engine whose label carries extra CLI
// arguments declares them under. A YAML load yields []any, a Go-built body
// []string; anything absent, not a list, or holding a non-string names none.
func TestLabelArgs_ReadsTheNeutralKey(t *testing.T) {
	for name, tc := range map[string]struct {
		body map[string]any
		want []string
	}{
		"yaml list":   {map[string]any{"args": []any{"--debug", "-v"}}, []string{"--debug", "-v"}},
		"string list": {map[string]any{"args": []string{"--x"}}, []string{"--x"}},
		"empty":       {map[string]any{"args": []any{}}, nil},
		"absent":      {map[string]any{"model": "x"}, nil},
		"nil body":    {nil, nil},
		"not a list":  {map[string]any{"args": "--debug"}, nil},
		"not text":    {map[string]any{"args": []any{"--ok", 7}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, LabelArgs(tc.body))
		})
	}
}
