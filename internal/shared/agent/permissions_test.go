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

// TestPermissionMode_StringOutOfRangeIsVisible pins the fix: String()'s
// default arm rendered ANY out-of-range value (a corrupted wire int, e.g. from
// a future enum member this build doesn't know about) as the string "default"
// — indistinguishable from an intentional PermissionDefault posture. A bad
// value must be visibly bad, not silently read as the safe default.
func TestPermissionMode_StringOutOfRangeIsVisible(t *testing.T) {
	assert.Equal(t, "default", PermissionDefault.String(), "the real default still spells \"default\"")

	bogus := PermissionMode(99)
	got := bogus.String()
	assert.NotEqual(t, "default", got, "an out-of-range value must not render as the legitimate default")
	assert.Contains(t, got, "99", "the bad value should name itself for diagnosis")
}

func TestPermissionModeNames(t *testing.T) {
	assert.Equal(t, []string{"default", "acceptEdits", "plan", "bypass"}, PermissionModeNames())
}

// TestWireMode pins the decode fallback: empty and unknown input become the safe
// PermissionDefault (prompt) while a known value round-trips. A mutant that
// changed the fallback to any other posture would flip a real safety default.
func TestWireMode(t *testing.T) {
	assert.Equal(t, PermissionDefault, WireMode(""))
	assert.Equal(t, PermissionDefault, WireMode("nonsense"))
	assert.Equal(t, PermissionBypass, WireMode("bypass"))
	assert.Equal(t, PermissionPlan, WireMode("plan"))
}

// TestResolveDefault pins the shared base resolution: first parseable source
// wins; otherwise claude-code falls to bypass (host stopgap) and everything else
// to default (prompt).
func TestResolveDefault(t *testing.T) {
	// First parseable wins, in order.
	assert.Equal(t, PermissionPlan, ResolveDefault([]string{"plan", "bypass"}, true))
	assert.Equal(t, PermissionBypass, ResolveDefault([]string{"", "bypass", "plan"}, false))
	// An unparseable source is skipped, not treated as a request.
	assert.Equal(t, PermissionAcceptEdits, ResolveDefault([]string{"nonsense", "acceptEdits"}, true))
	// Nothing set: claude-code → bypass, others → default.
	assert.Equal(t, PermissionBypass, ResolveDefault([]string{"", ""}, true))
	assert.Equal(t, PermissionDefault, ResolveDefault([]string{"", ""}, false))
	assert.Equal(t, PermissionDefault, ResolveDefault(nil, false))
}

// TestCollapsePlanIfUnenforced verifies plan collapses to default only on a
// backend that can't enforce it as read-only; every other combination is
// unchanged.
func TestCollapsePlanIfUnenforced(t *testing.T) {
	assert.Equal(t, PermissionDefault, PermissionPlan.CollapsePlanIfUnenforced(false), "plan collapses where unenforced")
	assert.Equal(t, PermissionPlan, PermissionPlan.CollapsePlanIfUnenforced(true), "plan kept where enforced")
	// Non-plan postures are never touched, regardless of enforcement.
	for _, m := range []PermissionMode{PermissionDefault, PermissionAcceptEdits, PermissionBypass} {
		assert.Equal(t, m, m.CollapsePlanIfUnenforced(false), "%q unchanged (unenforced)", m)
		assert.Equal(t, m, m.CollapsePlanIfUnenforced(true), "%q unchanged (enforced)", m)
	}
}
