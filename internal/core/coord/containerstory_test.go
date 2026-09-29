package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
)

// containerStoryBackend is the key a container spawn hands isolation: the plan's
// BACKEND (the engine), never its AgentName. The container story is keyed on the
// engine — isolation.engineContainerSpecFor maps each registered engine to
// its declared container spec — so an agent NAME (or a
// label, or the empty string the deleted image-only constructors passed) hits
// the table's fail-closed default arm and the run dies at PrepareWorkspace's
// container gate with "no container story is declared for this engine".
//
// It lives in an UNTAGGED file so the docker-gated spawners
// (container_direct/container_progress, build tag docker_integration) and the
// pin below share one definition: the property is about which FIELD is read,
// which no docker daemon is needed to check.
func containerStoryBackend(plan *SpawnPlan) string {
	return plan.Backend
}

// TestContainerStoryBackend_KeysOnEngineNotAgentName pins the fix for the
// mislabeled lookup: the container spawners used to key the container spec on plan.AgentName
// (via a constructor that had already fixed it at ""), so a perfectly
// well-formed plan could not run in a container at all. Both halves are asserted —
// that the key IS the plan's engine and that engine declares a container story, and
// that the agent name does NOT, which is what made the old keying fail closed.
func TestContainerStoryBackend_KeysOnEngineNotAgentName(t *testing.T) {
	plan := &SpawnPlan{AgentName: "fast-worker", Backend: "mock", Runtime: "container"}

	key := containerStoryBackend(plan)

	require.Equal(t, plan.Backend, key,
		"a container spawn keys the container spec on the plan's Backend (the engine), not on %q", plan.AgentName)
	assert.True(t, isolation.HasContainerStory(key),
		"the key handed to isolation must name an engine with a container story; %q does not", key)
	assert.False(t, isolation.HasContainerStory(plan.AgentName),
		"precondition: an AGENT name is not an engine — keying on it reaches the fail-closed default and aborts PrepareWorkspace")
}
