package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// TestServeSpawnAgent_DirtyTreeHandlerParsedAtTheVerb pins the verb's edge:
// agent_run's input is free-form and model-filled, so the dirty_tree_handler
// spelling is validated HERE, once, and an unrecognized one is refused
// (ErrInvalidRequest) at the verb the caller invoked; the wire's own refusal
// of a non-string value is the codec's (its suite pins it).
//
// It matters because the value's unset path defaults to the "commit"
// handler, which auto-commits the parent's working tree: a spelling that
// resolved to the default would write to the user's repository past both the
// caller's and the project's explicit choice.
func TestServeSpawnAgent_DirtyTreeHandlerParsedAtTheVerb(t *testing.T) {
	newWorker := func(t *testing.T) (*fakeSpawner, *Coordinator) {
		t.Helper()
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
		return sp, newTestCoordinator(t, sp, nil)
	}

	// The vacuity guard: a well-spelled value takes this exact path all the
	// way onto the launched plan, so a refusal below is a refusal of the
	// SPELLING and not of some unrelated precondition.
	t.Run("control: a declared member reaches the spawned plan", func(t *testing.T) {
		sp, c := newWorker(t)
		reply := c.serveAgentRequest(ownerIdentity(), AgentRequest{Kind: SpawnRequest{Agent: "worker", Prompt: "task", DirtyTree: "stale"}})
		require.NoError(t, reply.Err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Equal(t, launch.DirtyTreeHandlerStale, sp.lastDirtyTreeHandler())
	})

	t.Run("a typo is refused at the verb and spawns nothing", func(t *testing.T) {
		sp, c := newWorker(t)
		reply := c.serveAgentRequest(ownerIdentity(), AgentRequest{Kind: SpawnRequest{Agent: "worker", Prompt: "task", DirtyTree: "fial"}})
		require.ErrorIs(t, reply.Err, ErrInvalidRequest)
		assert.Contains(t, reply.Err.Error(), "fial", "the refusal quotes what the caller typed")
		assert.Contains(t, reply.Err.Error(), "commit|copy|stale|fail", "and names the legal values")
		assert.Equal(t, 0, sp.spawnCount(), "no child may be launched for a refused spawn")
	})

	// The workspace axis rides the same edge, for the same reason plus one:
	// its axes are resolved on the LAUNCH goroutine, so without this parse a
	// typo is reported as a child that died rather than as a bad argument.
	t.Run("a typo'd workspace is refused at the verb too", func(t *testing.T) {
		sp, c := newWorker(t)
		reply := c.serveAgentRequest(ownerIdentity(), AgentRequest{Kind: SpawnRequest{Agent: "worker", Prompt: "task", Workspace: "wroktree"}})
		require.ErrorIs(t, reply.Err, ErrInvalidRequest)
		assert.Contains(t, reply.Err.Error(), "wroktree")
		assert.Contains(t, reply.Err.Error(), "none|worktree")
		assert.Equal(t, 0, sp.spawnCount())
	})

	// THE UNSET PATH, unchanged: a caller that says nothing carries no
	// override, and the project default still decides downstream.
	t.Run("omitting the key still carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		reply := c.serveAgentRequest(ownerIdentity(), AgentRequest{Kind: SpawnRequest{Agent: "worker", Prompt: "task"}})
		require.NoError(t, reply.Err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastDirtyTreeHandler())
	})
}
