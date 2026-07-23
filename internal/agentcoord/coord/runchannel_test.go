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
	out, err := c.AgentRun(context.Background(), ownerIdentity(), "researcher", "find the thing", "", "")
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

	msgs := recvBody(t, c, "found it", time.Second)
	require.Len(t, msgs, 1)
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

	assert.Len(t, recvKind(t, c, "", time.Second), 1, "exactly one message was queued")
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

	// naval-snarl #1 (2026-07-22/23): this assertion used to be ONE
	// require.Eventually spanning the FULL crash→loss-detect→terminateRun→
	// goTracked resumeChild→spawner.Resolve→enqueueRun→new engine spawn→new
	// Home dial-home→HelloAck→redeliver pipeline against a wall-clock guess
	// (crashRedeliverWait, widened to 20s by damp-pupil (2026-07-21) after it
	// flaked at 5s under real host contention). That band-aid is now
	// REMOVED: the real fix is the SAME S3 pattern already proven in
	// TestStartRun_ResumeUsesJournaledHarnessSessionID — replace the
	// wall-clock guess over the goroutine-scheduling-dependent PART of the
	// chain with awaitChildUp, which blocks on the tracked resumeChild
	// goroutine's OWN progress signal (armLaunch/markAttached) instead of
	// guessing how long scheduling takes under contention.
	//
	// Two stages, because awaitChildUp itself needs the resume to already
	// be ARMED (c.launchArmed[harp] populated, or a fresh rt.attached) to
	// have something to wait on — and arming happens asynchronously off
	// h.crash() (the coordinator's OWN disconnect-detection goroutine, not
	// this test's call stack), unlike AgentRun/AgentSend which arm
	// synchronously before returning. So stage 1 waits out just that short
	// window (disconnect-detect + terminateRun's one journal fsync +
	// severChan/pendingCount + armLaunch — no engine respawn yet, hence
	// still bounded by the shared conformanceWait); stage 2 (awaitChildUp)
	// then deterministically covers the actual respawn (spawner.Resolve,
	// enqueueRun's fsync, the fresh engine launch, and — legacy path —
	// sendTurn's in-channel handoff, all sequenced strictly before
	// markAttached closes the signal awaitChildUp is watching).
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if rt := c.byHarp[out.Harp]; rt != nil && rt.attached != nil {
			return true
		}
		return len(c.launchArmed[out.Harp]) > 0
	}, conformanceWait, 5*time.Millisecond, "the crash must be detected and the resume armed")

	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), conformanceWait)
	defer awaitCancel()
	require.NoError(t, c.awaitChildUp(awaitCtx, out.Harp), "the resumed run must attach")
	require.Equal(t, 2, sp.spawnCount(), "the crash must have respawned the harp")

	// The only thing left unresolved once awaitChildUp returns is the
	// fakeEngine's own in-process channel handoff (sendTurn's rendezvous
	// send completing before the receiving goroutine appends to texts) —
	// µs-scale, not the multi-hop respawn above, so the shared
	// conformanceWait bound (not the removed 20s one) is ample.
	require.Eventually(t, func() bool {
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
