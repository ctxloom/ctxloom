package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestResolveAgentSurfaces_AcceptsWhatTheEngineDeclares: system-prompt is
// claude's, and claude is where it should be settable — it is the lowest-
// interaction delivery there is (a flag on the command line; no project file
// touched, no process of ours started).
func TestResolveAgentSurfaces_AcceptsWhatTheEngineDeclares(t *testing.T) {
	got, err := ResolveAgentSurfaces("claude-code", map[string]string{"context": "system-prompt"})
	require.NoError(t, err)
	assert.Equal(t, map[agent.SurfaceKind]agent.Approach{
		agent.SurfaceContext: agent.ApproachSystemPrompt,
	}, got)
}

// TestResolveAgentSurfaces_RefusesAnApproachTheEngineDoesNotHave is the claim
// this validator exists for. system-prompt is claude-only, so a kiro agent
// naming it has made a mistake — and the refusal must NAME what kiro can do,
// because "unsupported" alone leaves the user to guess.
//
// Refused, never downgraded: silently substituting the engine's default would
// teach the caller their request worked, and their sessions would deliver
// context a way they did not choose.
func TestResolveAgentSurfaces_RefusesAnApproachTheEngineDoesNotHave(t *testing.T) {
	for _, engine := range []string{"kiro", "opencode", "codex"} {
		got, err := ResolveAgentSurfaces(engine, map[string]string{"context": "system-prompt"})
		require.Error(t, err, "%s does not declare system-prompt", engine)
		assert.Nil(t, got, "%s: a refused preference must resolve to nothing, never a partial map", engine)
		assert.Contains(t, err.Error(), "system-prompt")
		assert.Contains(t, err.Error(), "supports:", "%s: the error must name what this engine CAN do", engine)
	}
}

// Names that exist nowhere are refused before any engine is consulted, so the
// message is about the typo rather than about an engine's capabilities.
func TestResolveAgentSurfaces_RefusesUnknownNames(t *testing.T) {
	_, err := ResolveAgentSurfaces("claude-code", map[string]string{"kontext": "hook"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kontext")

	_, err = ResolveAgentSurfaces("claude-code", map[string]string{"context": "telepathy"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "telepathy")
}

// An unknown ENGINE is an error rather than an empty allowance. Returning "no
// approaches" for a name nobody registered would make every pair unsupported
// and report it as the engine's limitation instead of as a bad engine name.
func TestResolveAgentSurfaces_RefusesAnUnknownEngine(t *testing.T) {
	_, err := ResolveAgentSurfaces("no-such-engine", map[string]string{"context": "hook"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-engine")
}

// Nothing declared is not an error and resolves to nothing: the overwhelmingly
// common agent has no preference and takes the engine's default.
func TestResolveAgentSurfaces_EmptyIsNotAPreference(t *testing.T) {
	got, err := ResolveAgentSurfaces("claude-code", nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}
