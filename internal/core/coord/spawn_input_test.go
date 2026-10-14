package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeSpawnAgent_WorkspaceParsedAtTheVerb pins the verb's edge:
// agent_run's input is free-form and model-filled, and its workspace axis is
// resolved on the LAUNCH goroutine, so without a parse here a typo is
// reported as a child that died rather than as a bad argument. The spelling
// is validated HERE, once, and an unrecognized one is refused
// (ErrInvalidRequest) at the verb the caller invoked; the wire's own refusal
// of a non-string value is the codec's (its suite pins it).
func TestServeSpawnAgent_WorkspaceParsedAtTheVerb(t *testing.T) {
	newWorker := func(t *testing.T) (*fakeSpawner, *Coordinator) {
		t.Helper()
		resetStrictness(t)
		sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
		return sp, newTestCoordinator(t, sp, nil)
	}

	t.Run("a typo'd workspace is refused at the verb and spawns nothing", func(t *testing.T) {
		sp, c := newWorker(t)
		reply := c.serveAgentRequest(ownerIdentity(), AgentRequest{Kind: SpawnRequest{Agent: "worker", Prompt: "task", Workspace: "wroktree"}})
		require.ErrorIs(t, reply.Err, ErrInvalidRequest)
		assert.Contains(t, reply.Err.Error(), "wroktree", "the refusal quotes what the caller typed")
		assert.Contains(t, reply.Err.Error(), "none|worktree", "and names the legal values")
		assert.Equal(t, 0, sp.spawnCount(), "no child may be launched for a refused spawn")
	})

	// The unset path: a caller that says nothing carries no override, and the
	// project default decides downstream.
	t.Run("omitting the key carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		reply := c.serveAgentRequest(ownerIdentity(), AgentRequest{Kind: SpawnRequest{Agent: "worker", Prompt: "task"}})
		require.NoError(t, reply.Err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastWorkspace())
	})
}
