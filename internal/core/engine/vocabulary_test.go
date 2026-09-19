package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The engine vocabulary: Name is the registry key and the only spelling of an
// engine; Mode is how a run is driven; PermissionMode is the launch-time
// posture every engine maps. These pin the VALUES and the round-trip through
// the one parser, so a caller that spells a posture and one that parses it
// cannot disagree.

func TestName_IsAStringKey(t *testing.T) {
	n := Name("mock")
	assert.Equal(t, "mock", string(n))
	assert.Equal(t, "mock", n.String())
}

func TestMode_TwoWaysToDriveARun(t *testing.T) {
	assert.NotEqual(t, Interactive, Structured)
	assert.Equal(t, Mode(0), Interactive, "the zero value is Interactive: the wire enum and every unset caller say so")
	assert.Equal(t, Mode(1), Structured)
	assert.Equal(t, "interactive", Interactive.String())
	assert.Equal(t, "structured", Structured.String())
	assert.Equal(t, "mode(99)", Mode(99).String(), "an unknown mode must be visibly bad, not silently one of the two")
}

func TestPermissionMode_ZeroIsNotRequested(t *testing.T) {
	// Named numerically on purpose: the zero must render as "not requested"
	// and default must be 1, whatever the constants are called.
	assert.Equal(t, "not requested", PermissionMode(0).String(), "the zero value asks for nothing and renders as what it is, never as one of the four modes")
	assert.Equal(t, PermissionMode(1), PermissionDefault, "default is a POSTURE a caller can ask for by name, so it cannot share the unset value")
	for _, n := range PermissionModeNames() {
		assert.NotEqual(t, n, PermissionMode(0).String(), "the zero is not a mode a caller can spell")
	}
	_, ok := ParsePermissionMode(PermissionMode(0).String())
	assert.False(t, ok, "the zero is not a declaration and must not parse as one")

	m, ok := ParsePermissionMode("default")
	require.True(t, ok)
	assert.Equal(t, PermissionDefault, m, "an explicit \"default\" is a declaration, distinguishable from nothing")
	assert.NotEqual(t, PermissionMode(0), m)

	m, ok = ParsePermissionMode("")
	assert.False(t, ok)
	assert.Equal(t, PermissionMode(0), m, "unset parses to the zero, not to a posture")
}

func TestPermissionMode_StringAndParse_RoundTrip(t *testing.T) {
	for _, m := range []PermissionMode{PermissionDefault, PermissionAcceptEdits, PermissionPlan, PermissionBypass} {
		got, ok := ParsePermissionMode(m.String())
		require.True(t, ok, "%s must parse back", m)
		assert.Equal(t, m, got)
	}
	assert.Equal(t, "permissionMode(42)", PermissionMode(42).String(), "a corrupted value must be visibly bad, not silently the default")
}

func TestPermissionMode_Names_AreTheParseableSpellings(t *testing.T) {
	names := PermissionModeNames()
	require.Len(t, names, 4)
	for _, n := range names {
		_, ok := ParsePermissionMode(n)
		assert.True(t, ok, "%q is advertised but does not parse", n)
	}
}

func TestParsePermissionMode_UnsetAndUnknown_AreNotDeclarations(t *testing.T) {
	for _, s := range []string{"", "   ", "plann", "yolo"} {
		m, ok := ParsePermissionMode(s)
		assert.False(t, ok, "%q must not count as a declaration", s)
		assert.Equal(t, PermissionDefault, m)
	}
	assert.Equal(t, PermissionPlan, WireMode("plan"))
	assert.Equal(t, PermissionDefault, WireMode("nonsense"), "the wire fallback is the prompting posture")
}

func TestPermissionMode_Predicates(t *testing.T) {
	assert.True(t, PermissionBypass.AllowsWithoutPrompt())
	assert.False(t, PermissionAcceptEdits.AllowsWithoutPrompt(), "acceptEdits is edit-scoped, not a blanket allow")
	assert.True(t, PermissionBypass.SafeHeadless())
	assert.True(t, PermissionPlan.SafeHeadless())
	assert.False(t, PermissionDefault.SafeHeadless())
	assert.Equal(t, PermissionDefault, PermissionPlan.CollapsePlanIfUnenforced(false))
	assert.Equal(t, PermissionPlan, PermissionPlan.CollapsePlanIfUnenforced(true))
	assert.Equal(t, PermissionBypass, PermissionBypass.CollapsePlanIfUnenforced(false), "only plan collapses")
	assert.Equal(t, PermissionPlan, PermissionFloor, "the floor is the most restrictive tier ctxloom can name")
}
