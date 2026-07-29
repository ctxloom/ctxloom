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

// TestThinkingLevel_NamesCoverEveryDeclaredLevel is the U102-F12 drift gate —
// the leg the two round-trip tests above leave open.
//
// The four spellings were written out THREE times in one 84-line file:
// String()'s switch, ParseThinkingLevel's switch, and ThinkingLevelNames'
// literal. String<->Parse round-tripping catches drift between the first two,
// and the literal-equality test above catches an edit to the third — but
// nothing catches a tier added to Parse and String while ThinkingLevelNames is
// left alone: `--thinking xhigh` would then parse, print as itself, and never
// appear in flag help.
//
// This closes it in both directions: every advertised name parses to a distinct
// level that prints back as itself, and every declared level is advertised.
func TestThinkingLevel_NamesCoverEveryDeclaredLevel(t *testing.T) {
	names := ThinkingLevelNames()

	byLevel := map[ThinkingLevel]string{}
	for _, name := range names {
		level, ok := ParseThinkingLevel(name)
		assert.True(t, ok, "ThinkingLevelNames advertises %q but ParseThinkingLevel rejects it", name)
		assert.Equal(t, name, level.String(), "%q must round-trip through Parse -> String", name)
		if prev, dup := byLevel[level]; dup {
			t.Errorf("%q and %q both map to the same level", prev, name)
		}
		byLevel[level] = name
	}

	for _, level := range []ThinkingLevel{ThinkingOff, ThinkingLow, ThinkingMedium, ThinkingHigh} {
		assert.Contains(t, names, level.String(),
			"level prints as %q, which flag help never advertises", level.String())
		assert.Contains(t, byLevel, level,
			"level %q is declared but no advertised name parses to it", level.String())
	}
}
