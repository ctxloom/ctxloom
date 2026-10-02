package engine

import (
	"errors"
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

// TestVersionCommand_CheckFloor: a version below the declared floor is
// refused with a typed error that names the floor and the remedy; at or above
// it passes; no floor checks nothing; a version that is not a version is
// refused, never guessed to be new enough.
func TestVersionCommand_CheckFloor(t *testing.T) {
	floored := VersionCommand{Floor: "2.1.283"}
	require.NoError(t, floored.CheckFloor("claude-code", "2.1.283"), "the floor itself is supported")
	require.NoError(t, floored.CheckFloor("claude-code", "2.2.0"))
	require.NoError(t, VersionCommand{}.CheckFloor("mock", ""), "no floor declared checks nothing")

	err := floored.CheckFloor("claude-code", "2.1.259")
	var below *BelowFloorError
	require.ErrorAs(t, err, &below)
	assert.Equal(t, BelowFloorError{Engine: "claude-code", Version: "2.1.259", Floor: "2.1.283"}, *below)
	assert.Contains(t, err.Error(), "2.1.283")
	assert.Contains(t, below.Remedy(), "upgrade")

	err = floored.CheckFloor("claude-code", "not-a-version")
	require.Error(t, err)
	assert.False(t, errors.As(err, &below), "an unparseable version is not a known-old one")
}
