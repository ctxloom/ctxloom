package coord

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAgentRun_AbortedSpawnReleasesTheHarp pins the accounting half of
// agent_run's refusal paths. Spawner.AssignSession COMMITS the child's harp
// to persistent session accounting (in production, a session directory and a
// per-session engine home on disk); the two steps that follow it —
// spawnReachURL and enqueueRun — each returned their error straight to the
// caller, so every refused spawn left a harp assigned to a child that will
// never exist. The verb reports failure and the accounting says a session
// started, forever.
//
// The release primitive is the one the Spawner already exposes:
// MarkSessionEnded, the same call every terminal path makes.
func TestAgentRun_AbortedSpawnReleasesTheHarp(t *testing.T) {
	t.Run("no reachable endpoint", func(t *testing.T) {
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
		// NOT served: ReachURL has no listener to advertise, so
		// spawnReachURL refuses (strictness is non-degraded) — the abort
		// AFTER AssignSession has already committed the harp.
		c, err := New(Options{ProjectDir: t.TempDir(), StateDir: t.TempDir(), Spawner: sp})
		require.NoError(t, err)
		t.Cleanup(c.Close)

		_, err = c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
		require.Error(t, err, "a spawn with no reachable coordinator endpoint must be refused")

		assigned := sp.assignedSessions()
		require.Len(t, assigned, 1, "the harp was assigned before the refusal (this test is about what happens to it)")
		assert.Equal(t, assigned, sp.endedSessions(),
			"the harp AssignSession committed must be released when the spawn aborts, or session accounting keeps a child that never existed")
	})

	t.Run("the enqueue journal fails", func(t *testing.T) {
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
		c := newTestCoordinator(t, sp, nil)
		// Closing the coordinator leaves the advertised loop URL resolvable
		// (spawnReachURL still succeeds) but fails the run journal, which is
		// the SECOND leak path: enqueueRun's error return.
		c.Close()

		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "do the thing", "", "")
		require.Error(t, err, "a spawn whose enqueue cannot be journaled must be refused")

		assigned := sp.assignedSessions()
		require.Len(t, assigned, 1, "the harp was assigned before the refusal")
		assert.Equal(t, assigned, sp.endedSessions(),
			"an enqueue that never journaled must still give the harp back")
	})
}
