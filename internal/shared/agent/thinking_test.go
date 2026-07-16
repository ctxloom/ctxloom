package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseThinkingLevel(t *testing.T) {
	cases := []struct {
		in   string
		want ThinkingLevel
		ok   bool
	}{
		{"off", ThinkingOff, true},
		{"low", ThinkingLow, true},
		{"medium", ThinkingMedium, true},
		{"high", ThinkingHigh, true},
		{"HIGH", ThinkingHigh, true},
		{"  low  ", ThinkingLow, true},
		{"", ThinkingMedium, false},
		{"nonsense", ThinkingMedium, false},
	}
	for _, tc := range cases {
		got, ok := ParseThinkingLevel(tc.in)
		assert.Equal(t, tc.want, got, "parse %q", tc.in)
		assert.Equal(t, tc.ok, ok, "ok %q", tc.in)
	}
}

// TestThinkingLevel_StringRoundTrips ensures every canonical value survives a
// String -> ParseThinkingLevel round trip, so the wire/config spelling and
// the parser never drift apart.
func TestThinkingLevel_StringRoundTrips(t *testing.T) {
	for _, l := range []ThinkingLevel{ThinkingOff, ThinkingLow, ThinkingMedium, ThinkingHigh} {
		got, ok := ParseThinkingLevel(l.String())
		assert.True(t, ok, "round-trip %q", l.String())
		assert.Equal(t, l, got)
	}
}

// TestThinkingLevel_ZeroValueIsMedium pins the deliberate const ordering: an
// unconfigured ThinkingLevel field (Go's int zero value) must be
// ThinkingMedium, the documented default -- NOT an implicit "off" a plain
// iota-from-off ordering would silently produce for any backend that never
// runs Configure.
func TestThinkingLevel_ZeroValueIsMedium(t *testing.T) {
	var zero ThinkingLevel
	assert.Equal(t, ThinkingMedium, zero)
	assert.Equal(t, "medium", zero.String())
}

func TestThinkingLevelNames(t *testing.T) {
	assert.Equal(t, []string{"off", "low", "medium", "high"}, ThinkingLevelNames())
}
