package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLabelArgs_ReadsTheNeutralKey: a label's args are read untyped from its
// body's args, the one key every engine whose label carries extra CLI
// arguments declares them under. A YAML load yields []any, a Go-built body
// []string; absent, null or empty names none, and anything not a list of
// strings is ErrMalformedLabelValue.
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
		"null":        {map[string]any{"args": nil}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := LabelArgs(tc.body)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	for name, v := range map[string]any{"not a list": "--debug", "not text": []any{"--ok", 7}, "a map": map[string]any{"a": "b"}} {
		t.Run(name, func(t *testing.T) {
			_, err := LabelArgs(map[string]any{"args": v})
			require.ErrorIs(t, err, ErrMalformedLabelValue, "args that are not a list of strings are refused, not read as none")
			require.ErrorContains(t, err, "args")
		})
	}
}
