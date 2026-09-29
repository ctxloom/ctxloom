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
	assert.Equal(t, PermissionMode(0), PermissionNotRequested, "the zero value asks for nothing: an unset launch.Source.Permission")
	assert.Equal(t, PermissionMode(1), PermissionDefault, "default is a POSTURE a caller can ask for by name, so it cannot share the unset value")
	assert.Equal(t, "not requested", PermissionNotRequested.String(), "the zero renders as what it is, never as one of the four modes")
	for _, n := range PermissionModeNames() {
		assert.NotEqual(t, n, PermissionNotRequested.String(), "NotRequested is not a mode a caller can spell")
	}
	_, ok := ParsePermissionMode(PermissionNotRequested.String())
	assert.False(t, ok, "NotRequested is not a declaration and must not parse as one")

	m, ok := ParsePermissionMode("default")
	require.True(t, ok)
	assert.Equal(t, PermissionDefault, m, "an explicit \"default\" is a declaration, distinguishable from nothing")
	assert.NotEqual(t, PermissionNotRequested, m)

	m, ok = ParsePermissionMode("")
	assert.False(t, ok)
	assert.Equal(t, PermissionNotRequested, m, "unset parses to the zero, not to a posture")
}

func TestPermissionMode_StringAndParse_RoundTrip(t *testing.T) {
	for _, m := range []PermissionMode{PermissionDefault, PermissionAcceptEdits, PermissionPlan, PermissionBypass, PermissionDontAsk, PermissionAuto} {
		got, ok := ParsePermissionMode(m.String())
		require.True(t, ok, "%s must parse back", m)
		assert.Equal(t, m, got)
	}
	assert.Equal(t, "permissionMode(42)", PermissionMode(42).String(), "a corrupted value must be visibly bad, not silently the default")
}

func TestPermissionMode_Names_AreTheParseableSpellings(t *testing.T) {
	names := PermissionModeNames()
	require.Len(t, names, 6)
	for _, n := range names {
		_, ok := ParsePermissionMode(n)
		assert.True(t, ok, "%q is advertised but does not parse", n)
	}
}

// TestPermissionMode_ClaudeSpellings pins the claude-aligned spellings of the
// two postures that exist only because claude names them: dontAsk (deny what
// the rules leave open) and auto (claude's classifier decides).
func TestPermissionMode_ClaudeSpellings(t *testing.T) {
	assert.Equal(t, "dontAsk", PermissionDontAsk.String())
	assert.Equal(t, "auto", PermissionAuto.String())
	for s, want := range map[string]PermissionMode{"dontAsk": PermissionDontAsk, "dontask": PermissionDontAsk, "dont-ask": PermissionDontAsk, "auto": PermissionAuto} {
		got, ok := ParsePermissionMode(s)
		require.True(t, ok, "%q must parse", s)
		assert.Equal(t, want, got, "%q", s)
	}
}

func TestParsePermissionMode_UnsetAndUnknown_AreNotDeclarations(t *testing.T) {
	for _, s := range []string{"", "   ", "plann", "yolo"} {
		m, ok := ParsePermissionMode(s)
		assert.False(t, ok, "%q must not count as a declaration", s)
		assert.Equal(t, PermissionNotRequested, m, "the mode is never a posture when ok is false")
	}
	assert.Equal(t, PermissionPlan, WireMode("plan"))
	assert.Equal(t, PermissionDefault, WireMode("nonsense"), "the wire fallback is the prompting posture")
	assert.Equal(t, PermissionDefault, WireMode(""), "the wire never hands back NotRequested: the sender resolved")
}

func TestPermissionMode_Predicates(t *testing.T) {
	assert.Equal(t, PermissionDefault, PermissionPlan.CollapsePlanIfUnenforced(false))
	assert.Equal(t, PermissionPlan, PermissionPlan.CollapsePlanIfUnenforced(true))
	assert.Equal(t, PermissionBypass, PermissionBypass.CollapsePlanIfUnenforced(false), "only plan collapses")
	assert.Equal(t, PermissionPlan, PermissionFloor, "the floor is the most restrictive tier ctxloom can name")
}
