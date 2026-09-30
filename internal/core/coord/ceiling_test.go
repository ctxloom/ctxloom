package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// runCeiling reads a run's journaled ceiling.
func runCeiling(c *Coordinator, runID string) string {
	var out string
	c.runs.View(func() {
		if r := c.runsF.run(runID); r != nil {
			out = r.Ceiling
		}
	})
	return out
}

// A child the root human's session launches is capped by nothing; a child
// that child launches is capped at the ceiling its parent resolved to, which
// the coordinator journals when the parent's launch resolves — so it is
// still known after a restart.
func TestAgentRun_GrandchildIsCappedAtItsParentsCeiling(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "acceptEdits"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)

	child, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "delegate this", "", "")
	require.NoError(t, err)
	childHome(t, c, child.RunID)
	assert.Equal(t, "acceptEdits", runCeiling(c, child.RunID), "the child's resolved ceiling is journaled")

	caller := c.inProject(Identity{Harp: child.Harp, RunID: child.RunID, Depth: 1})
	grand, err := c.AgentRun(context.Background(), caller, "worker", "go deeper", "", "")
	require.NoError(t, err)
	childHome(t, c, grand.RunID)

	assert.Equal(t, []engine.PermissionMode{engine.PermissionNotRequested, engine.PermissionAcceptEdits}, sp.resolvedParentCeilings(),
		"the root's child launches uncapped; the grandchild's launch is capped at its parent's ceiling")
}

// A child whose own ceiling the coordinator never recorded cannot have its
// children capped, so it may not launch any: failing closed beats an
// uncapped grandchild.
func TestAgentRun_UnknownParentCeilingIsRefused(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)

	_, err := c.AgentRun(context.Background(), Identity{Harp: "stranger", RunID: "run-nobody-knows", Depth: 1}, "worker", "go deeper", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ceiling")
	assert.Equal(t, 0, sp.spawnCount(), "nothing is launched for a refused spawn")
	assert.Empty(t, sp.resolvedParentCeilings())
}
