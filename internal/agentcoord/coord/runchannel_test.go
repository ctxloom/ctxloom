package coord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// Plane-2 conformance (B1.6 deliverable 2): the RunChannel against a LIVE
// coordinator endpoint, driven through the runner-side Home — reissue
// idempotency, parked-recv preemption, crash-between-notice-and-consume
// redelivery, timeout fallbacks, lineage-checked stop, and the report path.

// ownerHome registers a session-owner credential and opens a Home on it
// (Hello with an empty run_id — the owner attach).
func ownerHome(t *testing.T, c *Coordinator) *Home {
	t.Helper()
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)
	h, err := NewHome(context.Background(), HomeConfig{
		URL:     c.LoopbackURL(),
		Token:   token,
		RunID:   "", // depth-0: the channel attaches to the owning session
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	return h
}

// childHome opens a Home on a spawned child's runner env (its credential +
// run id from the per-spawn seam).
func childHome(t *testing.T, c *Coordinator, runID string) *Home {
	t.Helper()
	env := waitForChildEnv(t, c, runID)
	h, err := NewHome(context.Background(), HomeConfig{
		URL:     env[EnvCoordURL],
		Token:   env[EnvCoordCred],
		RunID:   env[EnvRunID],
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	return h
}

func spawnResearcher(t *testing.T, c *Coordinator) *RunOutcome {
	t.Helper()
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "researcher", "find the thing")
	require.NoError(t, err)
	return out
}

func researcherSpawner() *fakeSpawner {
	return newFakeSpawner(map[string]fakeAgent{
		"researcher": {perm: "bypass", profiles: []string{"p1"}},
	}, nil)
}

// TestRunChannel_ChildSendReachesParent: a child's plane-2 PeerSendRequest
// (to_role "parent") lands in the parent's durable mailbox with the kind
// convention riding structured.kind.
func TestRunChannel_ChildSendReachesParent(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	structured, _ := structpb.NewStruct(map[string]any{"kind": "result"})
	resp, err := h.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole:     ParentAddress,
			Text:       "found it",
			Structured: structured,
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.NotEmpty(t, resp.GetPeerSend().GetMessageId())

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "found it", msgs[0].Body)
	assert.Equal(t, "result", msgs[0].Kind)
	assert.Equal(t, out.Harp, msgs[0].From)
}

// TestRunChannel_RequestIdempotency: a reissued request (same request_id)
// gets the CACHED response — one queued message, same message_id back.
func TestRunChannel_RequestIdempotency(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	mk := func() *agentcoordpb.AgentRequest {
		return &agentcoordpb.AgentRequest{
			RequestId: "req-fixed-1",
			Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
				ToRole: ParentAddress,
				Text:   "once",
			}},
		}
	}
	first, err := h.Request(context.Background(), mk())
	require.NoError(t, err)
	second, err := h.Request(context.Background(), mk())
	require.NoError(t, err)
	assert.Equal(t, first.GetPeerSend().GetMessageId(), second.GetPeerSend().GetMessageId(),
		"the reissued request_id returns the cached response, not a second delivery")

	msgs, err := c.AgentRecv(context.Background(), ownerIdentity(), time.Second)
	require.NoError(t, err)
	assert.Len(t, msgs, 1, "exactly one message was queued")
}

// TestRunChannel_ParkedRecvPushAndConsume: a parked runner-side recv is
// completed by a pushed notice; the cursor advances ONLY on the consume
// fact.
func TestRunChannel_ParkedRecvPushAndConsume(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	type recvOut struct {
		msgs []*agentcoordpb.PeerMessage
		err  error
	}
	got := make(chan recvOut, 1)
	go func() {
		msgs, err := h.Recv(context.Background(), conformanceWait)
		got <- recvOut{msgs, err}
	}()

	// The park must reach the coordinator before the send (push window).
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		ch := c.chans[out.Harp]
		return ch != nil && ch.parked
	}, conformanceWait, 10*time.Millisecond)

	disposition, err := c.AgentSend(ownerIdentity(), out.Harp, "", "hello child", nil, "")
	require.NoError(t, err)
	assert.Contains(t, disposition, "waiting agent_recv")

	var r recvOut
	select {
	case r = <-got:
	case <-time.After(conformanceWait):
		t.Fatal("parked recv never completed")
	}
	require.NoError(t, r.err)
	require.Len(t, r.msgs, 1)
	assert.Equal(t, "hello child", r.msgs[0].GetText())

	// Tentative until acknowledged: the message is still pending in the
	// fold (reserved in the runtime ledger). The NEXT recv is the cursor-ack
	// — its timeout is irrelevant to the acknowledgement.
	assert.Equal(t, 0, c.pendingCount(out.Harp), "pushed ids are reserved (delivered-but-unacked)")
	_, err = h.Recv(context.Background(), 50*time.Millisecond)
	require.ErrorIs(t, err, ErrRecvTimeout)
	require.Eventually(t, func() bool {
		var pending int
		c.mail.View(func() { pending = len(c.mailF.pendingFor(out.Harp)) })
		return pending == 0
	}, conformanceWait, 10*time.Millisecond, "the cursor-ack consume fact advances the durable cursor")
}

// TestRunChannel_CrashBeforeConsumeRedelivers: a runner that received a
// pushed message but died before the consume fact leaves the message
// deliverable — the at-least-once guarantee under push-down.
func TestRunChannel_CrashBeforeConsumeRedelivers(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	got := make(chan []*agentcoordpb.PeerMessage, 1)
	go func() {
		msgs, err := h.Recv(context.Background(), conformanceWait)
		if err == nil {
			got <- msgs
		}
	}()
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		ch := c.chans[out.Harp]
		return ch != nil && ch.parked
	}, conformanceWait, 10*time.Millisecond)

	_, err := c.AgentSend(ownerIdentity(), out.Harp, "", "fragile", nil, "")
	require.NoError(t, err)
	select {
	case msgs := <-got:
		require.Len(t, msgs, 1)
	case <-time.After(conformanceWait):
		t.Fatal("recv never completed")
	}

	// Crash before the cursor-ack: tear the channel down WITHOUT the clean
	// Close (which would ack) — the tentative reservation is released and
	// the message RE-DELIVERS: the terminal path sees the leftover mail and
	// resumes the harp as a fresh run with the message as its next turn
	// (at-least-once, end to end).
	h.crash()
	sp := c.spawner.(*fakeSpawner)
	require.Eventually(t, func() bool {
		if sp.spawnCount() < 2 {
			return false
		}
		for _, text := range sp.engine(1).recordedTexts() {
			if strings.Contains(text, "fragile") {
				return true
			}
		}
		return false
	}, conformanceWait, 10*time.Millisecond, "the unconsumed push re-delivers into the resumed run's first turn")
}

// TestRunChannel_RecvPreemptionAndTimeout: the newest recv preempts a parked
// one (typed error), and a timed-out recv fails with the recv-timeout
// contract.
func TestRunChannel_RecvPreemptionAndTimeout(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	firstErr := make(chan error, 1)
	go func() {
		_, err := h.Recv(context.Background(), conformanceWait)
		firstErr <- err
	}()
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.park != nil
	}, conformanceWait, 10*time.Millisecond)

	// The newer receive preempts the parked one...
	_, err := h.Recv(context.Background(), 50*time.Millisecond)
	// ...and, with no mail arriving, itself times out.
	require.ErrorIs(t, err, ErrRecvTimeout)

	select {
	case ferr := <-firstErr:
		require.ErrorIs(t, ferr, ErrRecvPreempted)
	case <-time.After(conformanceWait):
		t.Fatal("preempted recv never completed")
	}
}

// TestRunChannel_StopRunLineage: plane-2 stop_run (rev-7 D1) terminates the
// caller's own child and refuses a foreign run id.
func TestRunChannel_StopRunLineage(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	// A child cannot stop itself (its lineage owns nothing).
	child := childHome(t, c, out.RunID)
	resp, err := child.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, codes.PermissionDenied, resp.GetStatus().GetCode())

	// The parent may.
	resp, err = owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.True(t, resp.GetStopRun().GetExitedWithinGrace())

	rec := RunRecord{}
	c.runs.View(func() { rec = *c.runsF.run(out.RunID) })
	assert.True(t, rec.Ended)
	assert.Equal(t, CauseStopped, rec.Cause)
}

// TestRunChannel_RosterProjection: list_runs projects the roster fold with
// the harp as agent_id and the latest report summary.
func TestRunChannel_RosterProjection(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)
	child := childHome(t, c, out.RunID)

	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS,
		Text:  "halfway there",
	}, nil))

	resp, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	runs := resp.GetListRuns().GetRuns()
	require.Len(t, runs, 1)
	assert.Equal(t, out.Harp, runs[0].GetAgent().GetAgentId())
	assert.Equal(t, out.RunID, runs[0].GetRunId())
	assert.Contains(t, runs[0].GetLatestSummary(), "halfway there")
}

// TestRunChannel_ReportDurability: Report returns only after the facts are
// journaled (Ack-gated); artifact revisions are coordinator-assigned,
// monotonic, and content-addressed (unchanged sha ≠ new revision).
func TestRunChannel_ReportDurability(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	child := childHome(t, c, out.RunID)

	art := func(sha byte) *agentcoordpb.ArtifactProduced {
		return &agentcoordpb.ArtifactProduced{
			ArtifactId: "plan/x",
			Kind:       agentcoordpb.ArtifactKind_ARTIFACT_KIND_IMPLEMENTATION_PLAN,
			Name:       "x.plan.md",
			Sha256:     []byte{sha},
		}
	}
	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_CHECKPOINT, Text: "cp1",
	}, []*agentcoordpb.ArtifactProduced{art(1)}))
	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_FINAL, Text: "done",
	}, []*agentcoordpb.ArtifactProduced{art(1)})) // unchanged content
	require.NoError(t, child.Report(context.Background(), &agentcoordpb.Summary{
		Scope: agentcoordpb.Summary_SCOPE_FINAL, Text: "done v2",
	}, []*agentcoordpb.ArtifactProduced{art(2)})) // changed content

	assert.Contains(t, c.LatestReport(out.Harp), "done v2")
	arts := c.Artifacts(out.Harp)
	require.Len(t, arts, 1)
	assert.EqualValues(t, 2, arts[0].Revision, "unchanged sha did not mint a revision; changed sha did")
}

// TestRunChannel_ForeignRunIDRejected: a Hello presenting a run_id the
// credential does not own is rejected.
func TestRunChannel_ForeignRunIDRejected(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	env := waitForChildEnv(t, c, out.RunID)

	h, err := NewHome(context.Background(), HomeConfig{
		URL:     env[EnvCoordURL],
		Token:   env[EnvCoordCred],
		RunID:   "run-not-mine",
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })

	// The channel never attaches; a request cannot complete.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, rerr := h.Request(ctx, &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_ListRuns{ListRuns: &agentcoordpb.ListRunsRequest{}},
	})
	require.ErrorIs(t, rerr, ErrCoordinatorUnreachable)
}
