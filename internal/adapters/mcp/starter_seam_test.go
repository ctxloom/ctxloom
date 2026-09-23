package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/coordtest"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestStarterSeam_MockChildRidesTheSpool names the seam's contract: a
// coordinator built with a Starter double hands the PRODUCTION spawner an
// in-process runner, so `mock` — admitted for delegated children through
// that seam and nothing else — spawns as a StartRun child whose mail rides
// the spool in both directions. The runner double is the real runner half
// (EngineHost + Home) around the real mock backend; only the process
// boundary is faked.
func TestStarterSeam_MockChildRidesTheSpool(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{"worker": headlessAgent("p1")})
	runners := coordtest.NewRunners(engines.Registry())
	t.Cleanup(runners.Close)
	c, err := coord.New(coord.Options{
		Spawner: spawn.New(nil, fixtureApp(t, cfg), root, runners.Starter), ProjectDir: root, StateDir: t.TempDir(),
		OwnerHarp: "coordinator-harp",
	})
	require.NoError(t, err)
	require.NoError(t, coordgrpc.Serve(c))
	t.Cleanup(c.Close)
	owner := coord.Identity{Harp: "coordinator-harp", Depth: 0}

	out, err := c.AgentRun(context.Background(), owner, "worker", "go", "", "")
	require.NoError(t, err, "mock is admitted for delegated children through the Starter seam")
	home := runners.AwaitHome(t, out.RunID)
	engine := runners.AwaitEngine(t, 0)

	// Down: the owner's send is ONE file in the child's in/, delivered to the
	// engine as a turn and consumed by rename.
	_, err = c.AgentSend(owner, out.Harp, coord.KindMessage, "second task", nil, "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		for _, txt := range engine.Texts() {
			if strings.Contains(txt, "second task") {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the child's engine never received the owner's send as a turn")
	require.Eventually(t, func() bool {
		res, err := spool.Sweep(spool.NewHomeMapper(), out.Harp, spool.DirInConsumed)
		if err != nil {
			return false
		}
		for _, e := range res.Entries {
			if e.Message.Body == "second task" {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the delivered file must be consumed by rename in the child's own spool")

	// Up: the child's agent_send is a local file write that the coordinator
	// routes into the owner's agent_recv.
	resp, err := home.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: coord.ParentAddress, Text: "a finding", Kind: agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	var got []coord.Message
	require.Eventually(t, func() bool {
		msgs, rerr := c.AgentRecv(context.Background(), owner, 200*time.Millisecond)
		if rerr != nil {
			return false
		}
		for _, m := range msgs {
			if m.Body == "a finding" {
				got = append(got, m)
			}
		}
		return len(got) > 0
	}, 10*time.Second, 10*time.Millisecond, "the owner's agent_recv must be satisfied from the child's out/ spool")
	assert.Equal(t, out.Harp, got[0].From)
}
