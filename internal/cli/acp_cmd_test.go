package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestSessionModesFrom_ProfilesAndAgents: the mode list is default set,
// then profiles (each assembling just itself), then agents (each assembling
// its composed profile set, carrying its declared engine, namespaced so a
// agent can never collide with a profile of the same name).
func TestSessionModesFrom_ProfilesAndAgents(t *testing.T) {
	subs := []operations.AgentEntry{
		{Name: "reviewer", Engine: "fast", Profiles: []string{"r1", "r2"}},
		{Name: "docs", Profiles: []string{"d"}},
	}
	modes := sessionModesFrom([]string{"go", "reviewer"}, subs, "", []string{"go", "base"}, "")
	require.NotNil(t, modes)

	assert.Equal(t, acpagent.DefaultModeID, modes.Current)
	require.Len(t, modes.Available, 5)

	assert.Equal(t, acpagent.DefaultModeID, modes.Available[0].ID)
	assert.Equal(t, "default (go, base)", modes.Available[0].Name)
	assert.Nil(t, modes.Available[0].Profiles, "the default mode assembles the configured defaults")

	assert.Equal(t, "go", modes.Available[1].ID)
	assert.Equal(t, []string{"go"}, modes.Available[1].Profiles)

	// The agent "reviewer" coexists with the PROFILE "reviewer": namespaced id.
	assert.Equal(t, "agent:reviewer", modes.Available[3].ID)
	assert.Equal(t, "reviewer (agent)", modes.Available[3].Name)
	assert.Equal(t, []string{"r1", "r2"}, modes.Available[3].Profiles)
	assert.Equal(t, "fast", modes.Available[3].Engine)

	assert.Equal(t, "agent:docs", modes.Available[4].ID)
	assert.Empty(t, modes.Available[4].Engine)
}

// TestSessionModesFrom_CurrentSelection: the launch selection decides the
// current mode — agent beats profile beats default.
func TestSessionModesFrom_CurrentSelection(t *testing.T) {
	subs := []operations.AgentEntry{{Name: "reviewer", Profiles: []string{"r"}}}

	modes := sessionModesFrom([]string{"go"}, subs, "", nil, "reviewer")
	require.NotNil(t, modes)
	assert.Equal(t, "agent:reviewer", modes.Current)

	modes = sessionModesFrom([]string{"go"}, subs, "go", nil, "")
	require.NotNil(t, modes)
	assert.Equal(t, "go", modes.Current)
}

// TestSessionModesFrom_UnlistedInitialProfileAppended: a launch profile the
// loader does not list (e.g. a bundle profile) still becomes a selectable mode.
func TestSessionModesFrom_UnlistedInitialProfileAppended(t *testing.T) {
	modes := sessionModesFrom([]string{"go"}, nil, "kit#profiles/review", nil, "")
	require.NotNil(t, modes)
	assert.Equal(t, "kit#profiles/review", modes.Current)
	last := modes.Available[len(modes.Available)-1]
	assert.Equal(t, "kit#profiles/review", last.ID)
	assert.Equal(t, []string{"kit#profiles/review"}, last.Profiles)
}

// TestSessionModesFrom_Empty: nothing to advertise → no modes block at all.
func TestSessionModesFrom_Empty(t *testing.T) {
	assert.Nil(t, sessionModesFrom(nil, nil, "", nil, ""))
}

// TestSessionModesFrom_AgentsOnly: agents alone still advertise modes
// (default + agents) even with no installed profiles.
func TestSessionModesFrom_AgentsOnly(t *testing.T) {
	modes := sessionModesFrom(nil, []operations.AgentEntry{{Name: "docs", Profiles: []string{"d"}}}, "", nil, "")
	require.NotNil(t, modes)
	require.Len(t, modes.Available, 2)
	assert.Equal(t, "agent:docs", modes.Available[1].ID)
}
