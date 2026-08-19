package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// spawnInput builds agent_run's free-form input Struct — the channel a MODEL
// fills, with no wire-level constraint on what it may put in it.
func spawnInput(t *testing.T, fields map[string]any) *structpb.Struct {
	t.Helper()
	in, err := structpb.NewStruct(fields)
	require.NoError(t, err)
	return in
}

// TestServeSpawnAgent_DirtyTreeHandlerParsedAtTheVerb pins the wire edge:
// agent_run's input Struct is free-form and model-filled, so the
// dirty_tree_handler spelling is converted HERE, once, and an unrecognized
// one is refused with InvalidArgument at the verb the caller invoked.
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
		resp := c.serveSpawnAgent(ownerIdentity(), &agentcoordpb.SpawnAgentRequest{
			Role:  "worker",
			Input: spawnInput(t, map[string]any{"prompt": "task", "dirty_tree_handler": "stale"}),
		})
		require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Equal(t, operations.DirtyTreeHandlerStale, sp.lastDirtyTreeHandler())
	})

	t.Run("a typo is refused at the verb and spawns nothing", func(t *testing.T) {
		sp, c := newWorker(t)
		resp := c.serveSpawnAgent(ownerIdentity(), &agentcoordpb.SpawnAgentRequest{
			Role:  "worker",
			Input: spawnInput(t, map[string]any{"prompt": "task", "dirty_tree_handler": "fial"}),
		})
		assert.EqualValues(t, codes.InvalidArgument, resp.GetStatus().GetCode())
		assert.Contains(t, resp.GetStatus().GetMessage(), "fial", "the refusal quotes what the caller typed")
		assert.Contains(t, resp.GetStatus().GetMessage(), "commit|copy|stale|fail", "and names the legal values")
		assert.Equal(t, 0, sp.spawnCount(), "no child may be launched for a refused spawn")
	})

	// structpb.Value.GetStringValue() answers "" for every non-string kind,
	// which is byte-identical to omitting the key — and omitting it selects
	// the default that commits. Present-but-wrong-type is its own input.
	t.Run("a non-string value is refused rather than read as unset", func(t *testing.T) {
		sp, c := newWorker(t)
		resp := c.serveSpawnAgent(ownerIdentity(), &agentcoordpb.SpawnAgentRequest{
			Role:  "worker",
			Input: spawnInput(t, map[string]any{"prompt": "task", "dirty_tree_handler": true}),
		})
		assert.EqualValues(t, codes.InvalidArgument, resp.GetStatus().GetCode())
		assert.Contains(t, resp.GetStatus().GetMessage(), "must be a string")
		assert.Equal(t, 0, sp.spawnCount())
	})

	// THE UNSET PATH, unchanged: a caller that says nothing carries no
	// override, and the project default still decides downstream.
	t.Run("omitting the key still carries no override", func(t *testing.T) {
		sp, c := newWorker(t)
		resp := c.serveSpawnAgent(ownerIdentity(), &agentcoordpb.SpawnAgentRequest{
			Role:  "worker",
			Input: spawnInput(t, map[string]any{"prompt": "task"}),
		})
		require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		require.Eventually(t, func() bool { return sp.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
		assert.Empty(t, sp.lastDirtyTreeHandler())
	})
}
