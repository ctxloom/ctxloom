package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// TestAgentRun_WorkspaceOverrideThreadsToSpawnPlan is GAP 2's threading
// proof at the coordinator layer: the workspace string agent_run's caller
// supplies rides AgentRun -> SpawnPlan.Workspace -> the Spawner's
// Launch/StartEngine call, completely independent of the agent's OWN
// resolved runtime axis (fakeAgent declares no runtime here). Each case gets
// its own coordinator (rather than two AgentRun calls on one) so the D4
// single-slot cap can never queue the second spawn behind the first and
// race this assertion. Omitting the argument (the empty-string call)
// carries nothing — PrepareAgentChat is where an empty override falls back
// to the project's cfg.Workspace default (delegate_test.go pins that hop).
func TestAgentRun_WorkspaceOverrideThreadsToSpawnPlan(t *testing.T) {
	newWorker := func(t *testing.T) (*fakeSpawner, *Coordinator) {
		t.Helper()
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
		return sp, newTestCoordinator(t, sp, nil)
	}

	t.Run("override rides the plan the Spawner launches from", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "worktree", "")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Equal(t, launch.WorkspaceWorktree, sp.lastWorkspace())
	})

	t.Run("omitting workspace carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastWorkspace())
	})
}

// TestAgentRun_DirtyTreeHandlerOverrideThreadsToSpawnPlan is the identical
// threading proof as TestAgentRun_WorkspaceOverrideThreadsToSpawnPlan, for
// AgentRun's dirty_tree_handler override: AgentRun -> SpawnPlan.DirtyTreeHandler
// -> the Spawner's Launch/StartEngine call. Omitting it carries nothing —
// PrepareAgentChat is where an empty override falls back to the project's
// cfg.GetDirtyTreeHandler() default (delegate_test.go pins that hop).
func TestAgentRun_DirtyTreeHandlerOverrideThreadsToSpawnPlan(t *testing.T) {
	newWorker := func(t *testing.T) (*fakeSpawner, *Coordinator) {
		t.Helper()
		resetStrictness(t)
		sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
		return sp, newTestCoordinator(t, sp, nil)
	}

	t.Run("override rides the plan the Spawner launches from", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", launch.DirtyTreeHandlerStale)
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Equal(t, launch.DirtyTreeHandlerStale, sp.lastDirtyTreeHandler())
	})

	t.Run("omitting dirty_tree_handler carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		_, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
		require.NoError(t, err)
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastDirtyTreeHandler())
	})
}
