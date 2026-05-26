package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFormatHud_HarpDisplay covers the Phase 3.5.1 harp visibility
// surface: when CTXLOOM_SESSION_HARP is set in the env, formatHud must
// include the harp name in the statusline. Three cases — set, unset,
// empty — to pin the contract.
func TestFormatHud_HarpDisplay(t *testing.T) {
	t.Run("included_when_set", func(t *testing.T) {
		t.Setenv("CTXLOOM_SESSION_HARP", "swift-amber-falcon")
		out := formatHud(claudeSessionJSON{}, ctxloomHudInfo{})
		assert.Contains(t, out, "swift-amber-falcon")
		assert.Contains(t, out, "⌁", "harp section is prefixed with the ⌁ glyph")
	})

	t.Run("omitted_when_unset", func(t *testing.T) {
		t.Setenv("CTXLOOM_SESSION_HARP", "")
		out := formatHud(claudeSessionJSON{}, ctxloomHudInfo{})
		assert.NotContains(t, out, "⌁", "no harp ⇒ no ⌁ section")
	})

	t.Run("composes_with_other_sections", func(t *testing.T) {
		t.Setenv("CTXLOOM_SESSION_HARP", "bold-crimson-thunder")
		session := claudeSessionJSON{}
		session.Model.DisplayName = "Claude Sonnet"
		session.ContextWindow.UsedPercentage = 42
		session.Cost.TotalCostUSD = 1.23
		out := formatHud(session, ctxloomHudInfo{Profile: "developer", BundleCount: 5})
		assert.Contains(t, out, "Claude Sonnet")
		assert.Contains(t, out, "42%")
		assert.Contains(t, out, "$1.23")
		assert.Contains(t, out, "developer")
		assert.Contains(t, out, "5b")
		assert.Contains(t, out, "bold-crimson-thunder")
	})
}

// TestContextBar covers the small bar-rendering helper. Mostly a smoke
// test that the bar character counts match the percentage; included so
// a future change to barWidth catches a coverage regression.
func TestContextBar(t *testing.T) {
	cases := []struct {
		pct      float64
		filled   int // expected count of █
		unfilled int // expected count of ░
	}{
		{0, 0, 8},
		{50, 4, 4},
		{100, 8, 0},
		{125, 8, 0}, // clamped at barWidth
	}
	for _, tc := range cases {
		bar := contextBar(tc.pct)
		filled := 0
		unfilled := 0
		for _, r := range bar {
			switch r {
			case '█':
				filled++
			case '░':
				unfilled++
			}
		}
		assert.Equal(t, tc.filled, filled, "pct=%.0f filled", tc.pct)
		assert.Equal(t, tc.unfilled, unfilled, "pct=%.0f unfilled", tc.pct)
	}
}

// TestContextBarColor maps usage thresholds to the right color escape.
func TestContextBarColor(t *testing.T) {
	assert.Equal(t, colorGreen, contextBarColor(0))
	assert.Equal(t, colorGreen, contextBarColor(59))
	assert.Equal(t, colorYellow, contextBarColor(60))
	assert.Equal(t, colorYellow, contextBarColor(79))
	assert.Equal(t, colorRed, contextBarColor(80))
	assert.Equal(t, colorRed, contextBarColor(99))
}
