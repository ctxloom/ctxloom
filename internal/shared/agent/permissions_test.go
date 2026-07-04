package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePermissionMode(t *testing.T) {
	cases := []struct {
		in   string
		want PermissionMode
		ok   bool
	}{
		{"default", PermissionDefault, true},
		{"acceptEdits", PermissionAcceptEdits, true},
		{"accept-edits", PermissionAcceptEdits, true},
		{"plan", PermissionPlan, true},
		{"bypass", PermissionBypass, true},
		{"BYPASS", PermissionBypass, true},
		{"dangerously-skip-permissions", PermissionBypass, true},
		{"", PermissionDefault, false},
		{"nonsense", PermissionDefault, false},
	}
	for _, tc := range cases {
		got, ok := ParsePermissionMode(tc.in)
		assert.Equal(t, tc.want, got, "parse %q", tc.in)
		assert.Equal(t, tc.ok, ok, "ok %q", tc.in)
	}
}

// TestPermissionMode_StringRoundTrips ensures every canonical value survives a
// String → ParsePermissionMode round trip, so the wire/config spelling and the
// parser never drift apart.
func TestPermissionMode_StringRoundTrips(t *testing.T) {
	for _, m := range []PermissionMode{PermissionDefault, PermissionAcceptEdits, PermissionPlan, PermissionBypass} {
		got, ok := ParsePermissionMode(m.String())
		assert.True(t, ok, "round-trip %q", m.String())
		assert.Equal(t, m, got)
	}
}

func TestPermissionMode_Predicates(t *testing.T) {
	// Only bypass grants blanket allow-without-prompt.
	assert.True(t, PermissionBypass.AllowsWithoutPrompt())
	for _, m := range []PermissionMode{PermissionDefault, PermissionAcceptEdits, PermissionPlan} {
		assert.False(t, m.AllowsWithoutPrompt(), "AllowsWithoutPrompt %q", m)
	}

	// bypass and plan run headless without hanging; default/acceptEdits block.
	assert.True(t, PermissionBypass.SafeHeadless())
	assert.True(t, PermissionPlan.SafeHeadless())
	assert.False(t, PermissionDefault.SafeHeadless())
	assert.False(t, PermissionAcceptEdits.SafeHeadless())
}

func TestPermissionModeNames(t *testing.T) {
	assert.Equal(t, []string{"default", "acceptEdits", "plan", "bypass"}, PermissionModeNames())
}
