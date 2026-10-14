package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent_stop never reaches the caller's OWN run. An owner-owned run journals
// its own harp as its parent (the self-loop owner_run.go names), so a lineage
// check of the bare form `ParentHarp == caller.Harp` counts the top-level
// session among its own children. On 2026-10-14 a session (jumpy-mossy-gusto)
// swept its children with a bulk agent_stop once its milestone landed: the
// sweep stopped its one live child AND its own run, mid-turn — the very turn
// making the call — so the session's runner was closed under it, the run
// ended RUN_STATUS_CANCELLED, and the session exited. The stop warning then
// named the session itself as the interrupted "child".

// stopSelfFixture is a top-level owner run with one live child of its own.
func stopSelfFixture(t *testing.T) (c *Coordinator, owner Identity, ownerRunID, child string) {
	t.Helper()
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c = newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute // far past the test: a sweep that waits for it fails
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	t.Cleanup(cancel)

	const ownerHarp = "owner-harp"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	starter, started := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	out, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass"), false), starter, "do the thing")
	require.NoError(t, err)
	require.True(t, *started)

	var parentHarp string
	c.runs.View(func() { parentHarp = c.runsF.run(out.RunID).ParentHarp })
	require.Equal(t, ownerHarp, parentHarp, "precondition: an owner run journals its OWN harp as its parent — the trap being guarded")

	kid, err := c.AgentRun(context.Background(), owner, "worker", "go", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(context.Background(), kid.Harp))
	return c, owner, out.RunID, kid.Harp
}

func ownerRunEnded(c *Coordinator, runID string) bool {
	var ended bool
	c.runs.View(func() { ended = c.runsF.run(runID).Ended })
	return ended
}

// The bulk sweep stops the caller's children and never the caller.
func TestStopChildren_NeverStopsTheCallersOwnRun(t *testing.T) {
	c, owner, ownerRunID, child := stopSelfFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), drainWait)
	defer cancel()
	out, err := c.StopChildren(ctx, owner, "milestone landed; reclaiming the containers")
	require.NoError(t, err)

	require.Len(t, out, 1, "the sweep names only the caller's children, never the caller: %+v", out)
	assert.Equal(t, child, out[0].Harp)
	assert.Equal(t, StopOutcomeStopped, out[0].Outcome)
	assert.Never(t, func() bool { return ownerRunEnded(c, ownerRunID) }, 300*time.Millisecond, 20*time.Millisecond,
		"a bulk agent_stop ended the calling session's own run")
}

// The per-run arms refuse the caller's own harp / run_id as not a child.
func TestAgentStop_RefusesTheCallersOwnRun(t *testing.T) {
	c, owner, ownerRunID, _ := stopSelfFixture(t)

	_, err := c.AgentStop(owner, owner.Harp, "stop myself", 0)
	require.ErrorIs(t, err, ErrNotAChild, "agent_stop by harp on the caller's own harp")

	reply := c.serveStopRun(context.Background(), owner, StopRun{RunID: ownerRunID, Reason: "stop myself", Grace: time.Millisecond})
	require.ErrorIs(t, reply.Err, ErrNotAChild, "agent_stop by run_id on the caller's own run")

	assert.False(t, ownerRunEnded(c, ownerRunID), "a refused self-stop ended the caller's own run")
}
