package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// D5 (manly-grant (4)/(5)): the depth-2 echo — parent spawns a depth-1
// child, the child spawns its OWN depth-2 grandchild (through the exact
// SAME AgentRun/plane-2 SpawnAgentRequest path a root session uses — flat-
// hub, nothing grandchild-specific), a marker relays UP through two
// mailboxes (grandchild -> its direct parent, the child; child -> ITS
// parent, root), and durable lineage (RunStarted/RunRecord.parent_run_id)
// is journal-proven throughout. workerSpawner mirrors researcherSpawner's
// shape (runchannel_test.go) — the legacy (non-StartRun) path: this test is
// about plane-2/mailbox mechanics, which B1.6 already unified across both
// engine-hosting paths, so the simpler path is the faithful one to drive.
func workerSpawner() *fakeSpawner {
	return newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", profiles: []string{"p1"}},
	}, nil)
}

func TestDepthTwo_MarkerRelayedThroughTwoMailboxes(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, workerSpawner(), nil)

	// depth 0 -> depth 1
	child, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "delegate this")
	require.NoError(t, err)
	childH := childHome(t, c, child.RunID)

	// depth 1 -> depth 2: the CHILD spawns its own grandchild via the same
	// plane-2 SpawnAgentRequest a root session's agent_run rides — the
	// depth guard derives from childH's OWN credential (caller.Depth == 1),
	// so this is refused pre-D5 and accepted post-D5.
	input, err := structpb.NewStruct(map[string]any{"prompt": "go deeper"})
	require.NoError(t, err)
	spawnResp, err := childH.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentRequest{
			Role: "worker", Input: input,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, spawnResp.GetStatus().GetCode(), spawnResp.GetStatus().GetMessage())
	grandchildHarp := spawnResp.GetSpawnAgent().GetChildAgentId()
	grandchildRunID := spawnResp.GetSpawnAgent().GetChildRunId()
	require.NotEmpty(t, grandchildHarp)
	require.NotEmpty(t, grandchildRunID)

	// Durable lineage, journal-proven: the grandchild's own run record names
	// the CHILD as both parent harp and parent run_id (D5/manly-grant (5)) —
	// not the root session.
	var grandRec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.run(grandchildRunID); r != nil {
			cp := *r
			grandRec = &cp
		}
	})
	require.NotNil(t, grandRec)
	assert.Equal(t, child.Harp, grandRec.ParentHarp)
	assert.Equal(t, child.RunID, grandRec.ParentRunID)
	assert.Equal(t, 2, grandRec.Depth)

	grandchildH := childHome(t, c, grandchildRunID)

	// Hop 1: grandchild -> ITS OWN direct parent (the child), never root
	// directly — the peer model's flat-hub semantics (manly-grant (4)).
	sendResp, err := grandchildH.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "marker-from-grandchild",
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, sendResp.GetStatus().GetCode())

	msgs, err := childH.Recv(context.Background(), time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "the marker must land in the CHILD's mailbox, not root's")
	assert.Equal(t, "marker-from-grandchild", msgs[0].GetText())
	assert.Equal(t, grandchildHarp, msgs[0].GetFromAgentId())

	// Hop 2: the child relays up to ITS OWN parent (root) — an explicit,
	// agent-driven relay (the peer model dissolves multi-level trees this
	// way: no automatic pass-through).
	relayResp, err := childH.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "relayed: " + msgs[0].GetText(),
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, relayResp.GetStatus().GetCode())

	rootMsgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.Len(t, rootMsgs, 1)
	assert.Equal(t, "relayed: marker-from-grandchild", rootMsgs[0].Body)
	assert.Equal(t, child.Harp, rootMsgs[0].From, "root sees the RELAY's sender (the child), not the grandchild directly")
}
