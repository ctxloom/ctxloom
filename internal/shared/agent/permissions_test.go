package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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

// TestResolveDefault pins the shared base resolution: first declared source
// wins; otherwise claude-code falls to bypass (host stopgap) and everything else
// to default (prompt).
func TestResolveDefault(t *testing.T) {
	// First declared wins, in order.
	mode, honoured := ResolveDefault([]string{"plan", "bypass"}, true)
	assert.Equal(t, PermissionPlan, mode)
	assert.True(t, honoured)

	mode, honoured = ResolveDefault([]string{"", "bypass", "plan"}, false)
	assert.Equal(t, PermissionBypass, mode)
	assert.True(t, honoured)
}

// TestResolveDefault_UnsetIsUnchanged is the regression control for the
// unparseable floor below: an ABSENT declaration must keep resolving exactly as
// it always has — claude-code to its host-bypass stopgap, every other backend to
// prompt-per-call — and must report itself honoured, so no caller mistakes "no
// posture was declared" for "a declared posture was refused". Whitespace is
// absence, not a misspelling.
func TestResolveDefault_UnsetIsUnchanged(t *testing.T) {
	strictness.Reset()

	for _, sources := range [][]string{{"", ""}, nil, {}, {"   ", "\t"}} {
		mode, honoured := ResolveDefault(sources, true)
		assert.Equal(t, PermissionBypass, mode, "claude-code stopgap for %#v", sources)
		assert.True(t, honoured, "unset is honoured for %#v", sources)

		mode, honoured = ResolveDefault(sources, false)
		assert.Equal(t, PermissionDefault, mode, "non-claude prompt default for %#v", sources)
		assert.True(t, honoured, "unset is honoured for %#v", sources)
	}

	assert.Empty(t, strictness.All(), "an unset posture is not a fault and must record no finding")
}

// TestResolveDefault_UnparseableFloorsAndFails pins the silent-escalation fix:
// `permissions: plann` used to be SKIPPED like an unset source, so resolution
// walked on down the chain and landed on the claude-code host stopgap —
// bypass, i.e. --dangerously-skip-permissions, from a value that obviously
// meant the read-only posture. A declaration that missed now stops the chain at
// the most restrictive posture and records a fatal ClassConfig finding.
func TestResolveDefault_UnparseableFloorsAndFails(t *testing.T) {
	cases := []struct {
		name              string
		sources           []string
		claudeCodeDefault bool
	}{
		{"claude-code, nothing else declared", []string{"plann"}, true},
		{"a wider source below must not answer for it", []string{"plann", "bypass"}, true},
		{"non-claude backend floors the same way", []string{"plann"}, false},
		{"a later rung's typo floors too", []string{"", "yolo"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			strictness.Reset()

			mode, honoured := ResolveDefault(tc.sources, tc.claudeCodeDefault)

			assert.Equal(t, PermissionFloor, mode, "an unhonourable declaration floors to the most restrictive posture")
			assert.NotEqual(t, PermissionBypass, mode, "a typo must never resolve MORE privileged than what was typed")
			assert.False(t, honoured, "the caller must be told this posture was not honoured, so it applies no widening step")

			found := strictness.All()
			require.Len(t, found, 1, "exactly one fatal finding per unhonourable declaration")
			assert.Equal(t, strictness.ClassConfig, found[0].Class)
			assert.NotEmpty(t, found[0].FixIt, "a finding without a fix-it cannot be acted on")
		})
	}
}

// TestResolveDefault_UnparseableFloorsUnderDegraded: degraded mode disables
// FINDING collection, never the floor. A --degraded run is the one place the
// posture is actually launched with rather than aborted on, so it is exactly
// where the floor has to hold.
func TestResolveDefault_UnparseableFloorsUnderDegraded(t *testing.T) {
	strictness.Reset()
	strictness.SetDegraded(true)
	defer strictness.SetDegraded(false)

	mode, honoured := ResolveDefault([]string{"plann"}, true)
	assert.Equal(t, PermissionFloor, mode, "degraded narrows, it never widens")
	assert.False(t, honoured)
	assert.Empty(t, strictness.All(), "degraded mode collects no findings")
}

// TestPermissionFloorIsTheMostRestrictive pins WHICH posture the floor is: plan
// permits strictly less than every other tier (no mutation at all), so a
// mutation that points the floor at default/acceptEdits/bypass — each of which
// permits more — turns this red.
func TestPermissionFloorIsTheMostRestrictive(t *testing.T) {
	assert.Equal(t, PermissionPlan, PermissionFloor)
	assert.False(t, PermissionFloor.AllowsWithoutPrompt(), "the floor never auto-allows")
	assert.True(t, PermissionFloor.SafeHeadless(), "the floor must be launchable with no human in the loop")
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
