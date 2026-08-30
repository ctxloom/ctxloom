package coord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
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
// carried on the typed Kind field (coordination.proto field 7).
func TestRunChannel_ChildSendReachesParent(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	h := childHome(t, c, out.RunID)

	resp, err := h.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_PeerSend{PeerSend: &agentcoordpb.PeerSendRequest{
			ToRole: ParentAddress,
			Text:   "found it",
			Kind:   agentcoordpb.MessageKind_MESSAGE_KIND_RESULT,
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
				Kind:   agentcoordpb.MessageKind_MESSAGE_KIND_MESSAGE,
			}},
		}
	}
	first, err := h.Request(context.Background(), mk())
	require.NoError(t, err)
	second, err := h.Request(context.Background(), mk())
	require.NoError(t, err)
	assert.Equal(t, first.GetPeerSend().GetMessageId(), second.GetPeerSend().GetMessageId(),
		"the reissued request_id returns the cached response, not a second delivery")

	assert.Len(t, recvKind(t, c, KindMessage, time.Second), 1, "exactly one message was queued")
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

	disposition, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "hello child", nil, "")
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

	_, err := c.AgentSend(ownerIdentity(), out.Harp, KindMessage, "fragile", nil, "")
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

	// This assertion is split into two stages rather than ONE
	// require.Eventually spanning the FULL crash→loss-detect→terminateRun→
	// goTracked resumeChild→spawner.Resolve→enqueueRun→new engine spawn→new
	// Home dial-home→HelloAck→redeliver pipeline against a single wall-clock
	// guess: a fixed budget flakes under real host contention. awaitChildUp
	// instead blocks on the tracked resumeChild goroutine's OWN progress
	// signal (armLaunch/markAttached), deterministic regardless of how long
	// scheduling takes under contention.
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

	// The parent may, and its `reason` is HONOURED: it used to be
	// advertised in agent_stop's tool schema and discarded. It must reach the
	// run's durable terminal detail, and a second stop must report it back.
	resp, err = owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{
			RunId:  out.RunID,
			Reason: "superseded by a narrower brief",
		}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	rec := RunRecord{}
	c.runs.View(func() { rec = *c.runsF.run(out.RunID) })
	assert.True(t, rec.Ended)
	assert.Equal(t, CauseStopped, rec.Cause)
	assert.Contains(t, rec.Detail, "superseded by a narrower brief")

	// Stopping it again reports the recorded reason rather than the bare cause.
	resp, err = owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, codes.OK, resp.GetStatus().GetCode())
	assert.Contains(t, resp.GetStatus().GetMessage(), "superseded by a narrower brief")
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

// TestReleaseRunChan_ReconnectDoesNotStrandTentativeDeliveries is the
// regression guard: when a RunChannel RECONNECTS, the new stream
// registers itself under c.chans[harp] and cancels its predecessor in one c.mu
// window -- so by the time the OLD handler's deferred teardown runs, it always
// observes the successor and `registered := c.chans[harp] == ch` is FALSE.
// Teardown then discarded ch.pushed WITHOUT un-reserving it.
//
// That is permanent, silent loss. Reserved ids are invisible to
// undeliveredLocked and therefore to pendingCount, so the reattach push never
// re-sends them, and only unreserve ever clears c.delivered -- while agent_send
// had already reported success. Which goroutine won the race decided whether
// the message survived.
//
// Both channels here are synthetic and installed directly: the finding is about
// releaseRunChan's registration guard, and driving a real reconnect would make
// the assertion depend on push/ack timing rather than on the guard.
//
// The assertion is on the RESERVATION LEDGER (c.delivered), not on
// pendingCount, and the reserved id is synthetic. pendingCount also consults
// the durable fold, and a really-spawned child's own turn loop consumes its
// mail concurrently -- an earlier draft that queued a real message via
// AgentSend passed in isolation and failed inside the full package run for that
// reason, which is the wrong kind of red. The ledger is what this teardown owns
// and what the finding is about; that a reserved id is invisible to
// undeliveredLocked/pendingCount is already covered elsewhere.
func TestReleaseRunChan_ReconnectDoesNotStrandTentativeDeliveries(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	const harp, msgID = "reconnecting-child", "msg-must-not-be-stranded"

	// The channel the reconnect superseded still holds the message as a
	// tentative delivery; c.chans[harp] already points at its successor.
	_, cancelOld := context.WithCancel(context.Background())
	_, cancelNew := context.WithCancel(context.Background())
	superseded := &runChan{role: harp, pushed: []string{msgID}, cancel: cancelOld}
	successor := &runChan{role: harp, cancel: cancelNew}
	c.mu.Lock()
	c.chans[harp] = successor
	c.delivered[harp] = []string{msgID}
	c.mu.Unlock()

	require.Equal(t, []string{msgID}, reservedIDs(c, harp),
		"precondition: the id is reserved, so the message is invisible to redelivery")

	c.releaseRunChan(harp, superseded)

	assert.NotContains(t, reservedIDs(c, harp), msgID,
		"a superseded channel's tentative deliveries must be released so they re-deliver on the successor")

	c.mu.Lock()
	stillRegistered := c.chans[harp] == successor
	c.mu.Unlock()
	assert.True(t, stillRegistered,
		"tearing down a SUPERSEDED channel must not deregister the live successor")
}

// TestReleaseRunChan_AfterSeverChanIsANoOp pins why the removed `registered`
// guard is not load-bearing. severChan nils ch.pushed AND deletes the
// registration under c.mu, so the stream's own deferred teardown finds nothing
// left to release: the now-unguarded unreserve must be a no-op there, not a
// double release that disturbs the ledger severChan already settled.
func TestReleaseRunChan_AfterSeverChanIsANoOp(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	const harp, msgID = "severed-child", "msg-settled-by-severchan"

	_, cancel := context.WithCancel(context.Background())
	ch := &runChan{role: harp, pushed: []string{msgID}, cancel: cancel}
	c.mu.Lock()
	c.chans[harp] = ch
	c.delivered[harp] = []string{msgID}
	c.mu.Unlock()
	require.Equal(t, []string{msgID}, reservedIDs(c, harp))

	c.severChan(harp)
	require.NotContains(t, reservedIDs(c, harp), msgID, "severChan released the reservation synchronously")

	// The stream's deferred teardown then runs on the same, already-emptied
	// channel and must change nothing.
	c.releaseRunChan(harp, ch)
	assert.NotContains(t, reservedIDs(c, harp), msgID, "teardown after severChan is a no-op, not a double release")
}

// TestPushMail_SaturatedPumpReleasesTheDroppedReservation is the regression
// guard: pushMail reserved every selected id in the runtime
// delivery ledger BEFORE attempting the send, and the saturated-pump branch
// then dropped the notice while leaving the reservation standing. A reserved id
// is invisible to undeliveredLocked, so no later push -- not the next park, not
// the next agent_send -- ever re-selects it. On a channel that stays LIVE (the
// common case: a busy child, not a dying one) that is permanent silent loss of
// a message agent_send already reported delivered.
//
// releaseRunChan's unconditional un-reserve only rescues
// the message if the channel DIES; it is not a fix for this one.
//
// The channel is synthetic and its pump is UNBUFFERED with no reader, which is
// the saturated-pump condition stated exactly. The role is synthetic too, so no
// real child's turn loop competes for the mail.
func TestPushMail_SaturatedPumpReleasesTheDroppedReservation(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	const harp = "child-with-a-saturated-pump"

	msgID, _, err := c.queueMail(ownerIdentity().Harp, harp, "note", "do not strand me")
	if !assert.NoError(t, err) {
		return
	}

	_, cancel := context.WithCancel(context.Background())
	ch := &runChan{
		role:   harp,
		parked: true,
		send:   make(chan *agentcoordpb.CoordinatorFrame), // unbuffered, unread: every send hits `default`
		cancel: cancel,
	}
	c.mu.Lock()
	c.chans[harp] = ch
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		delete(c.chans, harp)
		c.mu.Unlock()
	})

	c.pushMail(harp)

	assert.NotContains(t, reservedIDs(c, harp), msgID,
		"a notice the pump refused was never delivered, so its id must not stay reserved")

	// The point of releasing it: the NEXT push must re-select the message once
	// the pump has room. A stranded reservation makes this push find nothing.
	c.mu.Lock()
	ch.send = make(chan *agentcoordpb.CoordinatorFrame, 1)
	drained := ch.send
	c.mu.Unlock()
	c.pushMail(harp)
	select {
	case frame := <-drained:
		assert.Equal(t, msgID, frame.GetNotice().GetPeerMessage().GetMessageId(),
			"the re-pushed notice carries the same message")
	default:
		assert.Fail(t, "the dropped message was never re-pushed after the pump drained")
	}
}

// TestPushMail_UnprojectableStructuredIsNotPushedHollow is the regression guard
// for the push-side twin of the servePeerSend case below. peerMessageProto swallowed
// the json.Unmarshal error on m.Structured (`if err == nil` with no else) and
// discarded structpb.NewStruct's error entirely, so a message whose structured
// payload could not be projected was pushed anyway -- as a PeerMessage carrying
// only `kind` and the body text.
//
// For the escalation ladder's relayed ApprovalRequest that is the whole
// message: the child receives an approval notice with no request to answer, and
// nothing anywhere reports a fault. Warning and leaving the message pending
// keeps it deliverable by the turn-boundary drain (which projects nothing)
// instead of burning it on a hollow notice.
func TestPushMail_UnprojectableStructuredIsNotPushedHollow(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	const harp = "child-awaiting-an-approval"

	// A JSON scalar, not an object: json.Unmarshal into map[string]any fails,
	// which is the error the old code discarded.
	msgID, _, err := c.queueMailPayload(ownerIdentity().Harp, harp, "approval_request", "approve?",
		json.RawMessage(`"not-an-object"`), "")
	if !assert.NoError(t, err) {
		return
	}

	_, cancel := context.WithCancel(context.Background())
	ch := &runChan{
		role:   harp,
		parked: true,
		send:   make(chan *agentcoordpb.CoordinatorFrame, 4),
		cancel: cancel,
	}
	c.mu.Lock()
	c.chans[harp] = ch
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		delete(c.chans, harp)
		c.mu.Unlock()
	})

	c.pushMail(harp)

	select {
	case frame := <-ch.send:
		pm := frame.GetNotice().GetPeerMessage()
		assert.Fail(t, "a message whose structured payload cannot be projected must not be pushed hollow",
			"pushed message_id=%q structured=%v", pm.GetMessageId(), pm.GetStructured().AsMap())
	default:
	}
	assert.NotContains(t, reservedIDs(c, harp), msgID,
		"a message that was never pushed must not be reserved as a tentative delivery")
}

// reservedIDs snapshots a role's runtime delivery ledger -- the ids handed to a
// RunChannel but not yet acked. An id sitting here is invisible to
// undeliveredLocked (and so to pendingCount and every redelivery path), which
// is exactly why leaking one is silent loss.
func reservedIDs(c *Coordinator, role string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.delivered[role]...)
}

// TestServePeerSend_UnmarshalableStructuredIsRefused is the regression guard:
// servePeerSend marshalled the caller's Struct with
// `if raw, merr := protojson.Marshal(s); merr == nil { structured = raw }` and
// NEVER inspected merr. On failure `structured` stayed nil and the message was
// queued WITHOUT its structured payload, reported as a successful send.
//
// For a parent answering a relayed approval that converts a decision into an
// unanswerable message: the decode side is strict and then reports "structured
// is required", attributing the fault to the sender, who was told the send
// succeeded. Refusing the send is what serveCustom two functions below already
// does with the identical protojson.Marshal failure.
//
// A Value with no oneof member set is the shape protojson rejects — the
// zero-value *structpb.Value a hand-built Struct can easily carry.
func TestServePeerSend_UnmarshalableStructuredIsRefused(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)

	unmarshalable := &structpb.Struct{Fields: map[string]*structpb.Value{"kind": {}}}
	require.Error(t, func() error { _, err := protojson.Marshal(unmarshalable); return err }(),
		"fixture check: this Struct must really be unmarshalable, or the test proves nothing")

	resp := c.servePeerSend(ownerIdentity(), &agentcoordpb.PeerSendRequest{
		ToAgentId: out.Harp, Text: "decision", Structured: unmarshalable,
	})
	assert.NotEqualValues(t, 0, resp.GetStatus().GetCode(),
		"a send whose structured payload cannot be carried must not report OK")
	assert.Empty(t, resp.GetPeerSend().GetMessageId(),
		"a refused send must not hand back a message id")

	var pending []Message
	c.mail.View(func() { pending = c.mailF.pendingFor(out.Harp) })
	for _, m := range pending {
		assert.NotEqual(t, "decision", m.Body,
			"a refused send must not queue a hollowed-out message")
	}
}

// TestServeStopRun_CancelsLaunch is plane-2 agent_stop's twin of
// Coordinator.AgentStop's own launch-cancellation fix: a stop that
// only ends the run record cannot stop a LAUNCHER — an armed relaunch or an
// in-flight container prepare (a seconds-wide window) carries on behind a
// response that already said "stopped". The host-side AgentStop verb calls
// cancelLaunch "on BOTH paths"; plane-2's
// serveStopRun (the path a coordinator-capable CHILD uses to stop its own
// grandchild) must call it too — a launcher reachable from either surface
// must be cancellable from either surface.
func TestServeStopRun_CancelsLaunch(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	require.False(t, c.launchStopped(out.Harp), "precondition: no stop has landed yet")

	resp, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode())

	assert.True(t, c.launchStopped(out.Harp),
		"plane-2 agent_stop (serveStopRun) must cancel the launch exactly like the host-side AgentStop verb — "+
			"an armed relaunch or an in-flight container prepare must turn back, not carry on behind a "+
			"response that already said \"stopped\"")
}

// TestServeStopRun_CancelsLaunch_EvenWhenAlreadyEnded reproduces the exact
// hazard shape on the plane-2 surface: a stop landing on a run
// that has ALREADY ended, with a relaunch armed behind it (simulated here by
// clearLaunchGate — exactly what a fresh agent_send/inject delivery to an
// ended child does). The already-ended early return must still cancel the
// launch, not just report "already ended" and leave the armed relaunch to
// carry on.
func TestServeStopRun_CancelsLaunch_EvenWhenAlreadyEnded(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, researcherSpawner(), nil)
	out := spawnResearcher(t, c)
	owner := ownerHome(t, c)

	first, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, first.GetStatus().GetCode())
	require.True(t, c.launchStopped(out.Harp), "precondition: the first stop already cancelled the launch")

	// Simulate a fresh delivery re-arming a relaunch (clearLaunchGate is
	// exactly what agent_send/inject call on a fresh ask to an ended child).
	c.clearLaunchGate(out.Harp)
	require.False(t, c.launchStopped(out.Harp), "precondition: the re-arm actually cleared the stop bit")

	second, err := owner.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_StopRun{StopRun: &agentcoordpb.StopRun{RunId: out.RunID}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, second.GetStatus().GetCode())
	assert.Contains(t, second.GetStatus().GetMessage(), "already ended", "sanity: this IS the already-ended branch")

	assert.True(t, c.launchStopped(out.Harp),
		"the already-ended branch must still cancel the launch — the exact 2026-07-24 incident shape: a stop "+
			"landing on an already-ended run with a relaunch armed behind it must not let that relaunch carry on")
}

// TestPushMail_TerminalDeliveryIsAPushTargetWhileUnparked pins the session
// owner's push target, which the ordinary unparked rule gets backwards.
//
// An unparked child is deliberately not pushed: its turn boundary owns that
// delivery, and pushing would strand the message in the runner's recv buffer.
// A runner advertising CapTerminalDelivery hosts no engine, so it has no turn
// boundary to own anything — and deliverNotice is what fires its terminal
// nudge. Withholding the push there does not defer the wake, it cancels it:
// the owner sits quiet until it happens to poll, which is the whole failure
// the terminal injector exists to end.
//
// Both arms are asserted because the gate has to keep telling them apart: an
// advertisement that stops being read, and one that starts applying to every
// unparked child, are different bugs with the same green.
func TestPushMail_TerminalDeliveryIsAPushTargetWhileUnparked(t *testing.T) {
	push := func(t *testing.T, harp string, caps map[string]bool) *agentcoordpb.CoordinatorFrame {
		t.Helper()
		c := newTestCoordinator(t, researcherSpawner(), nil)
		msgID, _, err := c.queueMail(ownerIdentity().Harp, harp, "report", "FINAL: the child finished")
		require.NoError(t, err)

		_, cancel := context.WithCancel(context.Background())
		ch := &runChan{
			role:   harp,
			parked: false, // never parked: nothing on this runner ever polls on its own
			send:   make(chan *agentcoordpb.CoordinatorFrame, 1),
			cancel: cancel,
			caps:   caps,
		}
		c.mu.Lock()
		c.chans[harp] = ch
		c.mu.Unlock()
		t.Cleanup(func() {
			c.mu.Lock()
			delete(c.chans, harp)
			c.mu.Unlock()
		})

		c.pushMail(harp)
		select {
		case frame := <-ch.send:
			require.Equal(t, msgID, frame.GetNotice().GetPeerMessage().GetMessageId(),
				"the pushed notice must carry the queued message")
			return frame
		default:
			return nil
		}
	}

	t.Run("advertised: the unparked owner is pushed", func(t *testing.T) {
		resetStrictness(t)
		frame := push(t, "session-owner-driving-a-terminal",
			map[string]bool{CapPeerMessaging: true, CapTerminalDelivery: true})
		assert.NotNil(t, frame,
			"a runner with no turn machinery must be pushed while unparked: the push IS the wake")
	})

	t.Run("not advertised: the unparked child is left to its turn boundary", func(t *testing.T) {
		resetStrictness(t)
		frame := push(t, "ordinary-unparked-child",
			map[string]bool{CapPeerMessaging: true})
		assert.Nil(t, frame,
			"an unparked child's turn-boundary drain owns the delivery; pushing would strand it in the recv buffer")
	})
}

// TestQueueMail_TerminalDeliveryOwnerIsPushedFromTheMailPath pins the SAME
// owner-push rule as its pushMail sibling above, but through the path
// production actually takes.
//
// The distinction is the whole point. queueMail decides pushability itself and
// only calls pushMail once it has already said yes, so pushMail's own guard is
// never reached from here — a terminal-delivery target present only there is
// dark in production while its direct-call test stays green. That is not
// hypothetical: it shipped, and a session owner's mail sat queued until the
// human happened to type something, because the wake that was supposed to
// prompt them only fired once they already had.
//
// So this test registers the channel BEFORE queueing and never calls pushMail:
// the frame has to arrive because the mail path chose to push it.
func TestQueueMail_TerminalDeliveryOwnerIsPushedFromTheMailPath(t *testing.T) {
	queue := func(t *testing.T, harp string, caps map[string]bool) *agentcoordpb.CoordinatorFrame {
		t.Helper()
		c := newTestCoordinator(t, researcherSpawner(), nil)

		_, cancel := context.WithCancel(context.Background())
		ch := &runChan{
			role:   harp,
			parked: false, // never parked: nothing on this runner ever polls on its own
			send:   make(chan *agentcoordpb.CoordinatorFrame, 1),
			cancel: cancel,
			caps:   caps,
		}
		c.mu.Lock()
		c.chans[harp] = ch
		c.mu.Unlock()
		t.Cleanup(func() {
			c.mu.Lock()
			delete(c.chans, harp)
			c.mu.Unlock()
		})

		// The registration above is the only setup; no pushMail call follows.
		msgID, _, err := c.queueMail(ownerIdentity().Harp, harp, "report", "FINAL: the child finished")
		require.NoError(t, err)

		select {
		case frame := <-ch.send:
			require.Equal(t, msgID, frame.GetNotice().GetPeerMessage().GetMessageId(),
				"the pushed notice must carry the queued message")
			return frame
		default:
			return nil
		}
	}

	t.Run("advertised: the mail path itself pushes the unparked owner", func(t *testing.T) {
		resetStrictness(t)
		frame := queue(t, "session-owner-driving-a-terminal",
			map[string]bool{CapPeerMessaging: true, CapTerminalDelivery: true})
		assert.NotNil(t, frame,
			"queueMail must push a terminal-delivery runner while unparked: it has no turn boundary, so the push IS the wake")
	})

	t.Run("not advertised: the unparked child is still left to its turn boundary", func(t *testing.T) {
		resetStrictness(t)
		frame := queue(t, "ordinary-unparked-child",
			map[string]bool{CapPeerMessaging: true})
		assert.Nil(t, frame,
			"an unparked child's turn-boundary drain still owns the delivery; the new target must not widen to every child")
	})
}
