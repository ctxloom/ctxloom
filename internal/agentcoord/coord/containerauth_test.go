package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// containerAuthBackend is the key a container spawn hands isolation: the plan's
// BACKEND (the engine), never its AgentName. Container auth is keyed on the
// engine — isolation.engineContainerSpecFor maps "claude-code"/"codex"/"kiro"/
// "opencode"/"mock" to their credential resolvers — so an agent NAME (or a
// label, or the empty string the deleted image-only constructors passed) hits
// the table's fail-closed default arm and the run dies at PrepareWorkspace's
// auth gate with "no container auth is registered for this engine".
//
// It lives in an UNTAGGED file so the docker-gated spawners
// (container_direct/container_progress, build tag docker_integration) and the
// pin below share one definition: the property is about which FIELD is read,
// which no docker daemon is needed to check.
func containerAuthBackend(plan *SpawnPlan) string {
	return plan.Backend
}

// TestContainerAuthBackend_KeysOnEngineNotAgentName pins the fix for the
// mislabeled lookup: the container spawners used to key auth on plan.AgentName
// (via a constructor that had already fixed it at ""), so a perfectly
// well-formed plan could not authenticate anything. Both halves are asserted —
// that the key IS the plan's engine and that engine has container auth, and
// that the agent name does NOT, which is what made the old keying fail closed.
func TestContainerAuthBackend_KeysOnEngineNotAgentName(t *testing.T) {
	plan := &SpawnPlan{AgentName: "fast-worker", Backend: "mock", Runtime: "container"}

	key := containerAuthBackend(plan)

	require.Equal(t, plan.Backend, key,
		"a container spawn keys auth on the plan's Backend (the engine), not on %q", plan.AgentName)
	assert.True(t, isolation.HasContainerAuth(key),
		"the key handed to isolation must name an engine with container auth; %q does not", key)
	assert.False(t, isolation.HasContainerAuth(plan.AgentName),
		"precondition: an AGENT name is not an engine — keying on it reaches the fail-closed default and aborts PrepareWorkspace")
}
