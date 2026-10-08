package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLabelContextWindow_ReadsTheNeutralKey: the per-label context window is
// read untyped from the body, as a YAML load (int) or a JSON-shaped body
// (float64) produces it; anything absent, non-numeric or negative declares
// none.
func TestLabelContextWindow_ReadsTheNeutralKey(t *testing.T) {
	for name, tc := range map[string]struct {
		body map[string]any
		want int
	}{
		"yaml int":   {map[string]any{"context_window": 131072}, 131072},
		"int64":      {map[string]any{"context_window": int64(65536)}, 65536},
		"uint64":     {map[string]any{"context_window": uint64(32768)}, 32768},
		"json float": {map[string]any{"context_window": float64(200000)}, 200000},
		"absent":     {map[string]any{"model": "x"}, 0},
		"nil body":   {nil, 0},
		"string":     {map[string]any{"context_window": "131072"}, 0},
		"negative":   {map[string]any{"context_window": -1}, 0},
		"other key":  {map[string]any{"contextWindow": 131072}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, LabelContextWindow(tc.body))
		})
	}
}
