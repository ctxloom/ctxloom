package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A binding's may_delegate is journaled with its run, and the run's own
// agent_run is checked against it: a listed role launches, any other is
// refused naming the roles it may launch, and nothing is resolved for it.
func TestAgentRun_MayDelegateRestrictsTheCaller(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"lead":   {perm: "bypass", mayDelegate: []string{"finder"}},
		"finder": {perm: "plan"},
		"coder":  {perm: "acceptEdits"},
	}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)

	lead, err := c.AgentRun(context.Background(), ownerIdentity(), "lead", "coordinate", "", "")
	require.NoError(t, err, "the root session's delegation is not restricted")
	childHome(t, c, lead.RunID)
	caller := c.inProject(Identity{Harp: lead.Harp, RunID: lead.RunID, Depth: 1})

	resolved := len(sp.resolved)
	_, err = c.AgentRun(context.Background(), caller, "coder", "write it", "", "")
	require.ErrorIs(t, err, ErrDelegationRefused)
	assert.ErrorContains(t, err, `agent "lead" may launch only finder, not "coder"`)
	assert.Len(t, sp.resolved, resolved, "nothing is resolved for a refused role")

	finder, err := c.AgentRun(context.Background(), caller, "finder", "look it up", "", "")
	require.NoError(t, err, "a listed role launches")
	childHome(t, c, finder.RunID)
}

// Unset may_delegate permits any role.
func TestAgentRun_NoMayDelegatePermitsAny(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"lead": {perm: "bypass"}, "coder": {perm: "acceptEdits"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	lead, err := c.AgentRun(context.Background(), ownerIdentity(), "lead", "coordinate", "", "")
	require.NoError(t, err)
	childHome(t, c, lead.RunID)
	coder, err := c.AgentRun(context.Background(), c.inProject(Identity{Harp: lead.Harp, RunID: lead.RunID, Depth: 1}), "coder", "write it", "", "")
	require.NoError(t, err)
	childHome(t, c, coder.RunID)
}
