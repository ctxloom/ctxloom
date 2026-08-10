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

// TestRunnerEnv_StampsDepth pins that runnerEnv stamps EnvRunDepth
// UNCONDITIONALLY — unlike the reach-back trio (EnvCoordURL/EnvCoordCred/
// EnvRunID), which is omitted whole when url == "" (a degraded launch),
// depth must always be present: leafness must not depend on reach-back
// being available.
func TestRunnerEnv_StampsDepth(t *testing.T) {
	withURL := runnerEnv("harp-1", "run-1", "tok", "http://127.0.0.1:1/mcp", 3)
	assert.Equal(t, "3", withURL[EnvRunDepth])

	degraded := runnerEnv("harp-1", "run-1", "tok", "", 0)
	assert.Equal(t, "0", degraded[EnvRunDepth], "depth is stamped even on a degraded (no reach-back) launch")
	assert.NotContains(t, degraded, EnvCoordURL, "the trio is still omitted whole on a degraded launch")
}

// TestEnqueueRun_DepthIncrementsFromCallerDepth pins the depth ARITHMETIC
// directly: a genuinely delegated child's depth is its CALLER's depth + 1,
// computed at the AgentRun call site (children.go) and threaded through as
// enqueueRun's explicit depth parameter — never re-derived inside
// enqueueRun itself, and never assumed to be a constant 1. A depth-1
// caller's child must land at depth 2, both on the journaled fact
// (readable back later as this run's own credential Identity, via
// applyEnqueued) and on the live childRt.
func TestEnqueueRun_DepthIncrementsFromCallerDepth(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass"}}, nil)
	c := newTestCoordinator(t, sp, nil)

	caller := Identity{Harp: "some-child", RunID: "run-child", Depth: 1}
	plan, err := c.spawner.Resolve(context.Background(), "worker")
	require.NoError(t, err)

	rt, _, err := c.enqueueRun(caller, plan, "grandchild-harp", "go deeper", false, make(chan struct{}), caller.Depth+1)
	require.NoError(t, err)
	assert.Equal(t, 2, rt.depth, "a depth-1 caller's child must be depth 2, not a hardcoded 1")

	var rec *RunRecord
	c.runs.View(func() {
		if r := c.runsF.run(rt.runID); r != nil {
			cp := *r
			rec = &cp
		}
	})
	require.NotNil(t, rec)
	assert.Equal(t, 2, rec.Depth, "the journaled fact must agree with the live childRt")
}

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
	// This test's whole point is exercising a depth-2 grandchild, which the
	// BUILT-IN default depth cap (1) now refuses — raise it here so the test
	// still exercises the peer-relay mechanics it targets, independent of
	// the built-in default's current value.
	c := newTestCoordinatorDepthCap(t, workerSpawner(), nil, 2)

	// depth 0 -> depth 1
	child, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "delegate this", "", "")
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

	// Select the grandchild's OWN send out of the child's mailbox: the
	// automatic result bridge (children.go's bridgeTurnResult) legitimately
	// delivers the grandchild's scripted turn output there too.
	var marker *agentcoordpb.PeerMessage
	require.Eventually(t, func() bool {
		msgs, rerr := childH.Recv(context.Background(), 50*time.Millisecond)
		if rerr != nil {
			return false
		}
		for _, m := range msgs {
			if m.GetText() == "marker-from-grandchild" {
				marker = m
			}
		}
		return marker != nil
	}, conformanceWait, 10*time.Millisecond, "the marker must land in the CHILD's mailbox, not root's")
	assert.Equal(t, grandchildHarp, marker.GetFromAgentId())

	// Hop 2: the child relays up to ITS OWN parent (root) — an explicit,
	// agent-driven relay (the peer model dissolves multi-level trees this
	// way: no automatic pass-through).
	relayResp, err := childH.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress, Text: "relayed: " + marker.GetText(),
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, relayResp.GetStatus().GetCode())

	// Select the RELAY (the child's own agent_send, kind "") out of the
	// parent's mailbox: bridged turn results (kind "result") legitimately
	// share it now — see children.go's bridgeTurnResult.
	rootMsgs := recvKind(t, c, "", time.Second)
	require.Len(t, rootMsgs, 1)
	assert.Equal(t, "relayed: marker-from-grandchild", rootMsgs[0].Body)
	assert.Equal(t, child.Harp, rootMsgs[0].From, "root sees the RELAY's sender (the child), not the grandchild directly")
}
