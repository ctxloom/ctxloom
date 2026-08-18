package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

const (
	kindHuman       = agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_HUMAN
	kindAgent       = agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_AGENT
	kindUnspecified = agentcoordpb.ControlInitiatorKind_CONTROL_INITIATOR_KIND_UNSPECIFIED
)

// humanInitiator is the viewer/terminal's initiator, spelled once.
func humanInitiator() ControlInitiator { return ControlInitiator{Kind: kindHuman} }

// TestControlInitiator_Validate pins the Kind/Harp pairing a bool used to make
// unrepresentable-by-accident. The unrecognised-kind row is the load-bearing
// one: an initiator this build does not know must be REFUSED, never defaulted
// into the narrower branch's privileges.
func TestControlInitiator_Validate(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   ControlInitiator
		ok   bool
	}{
		{"human without harp", ControlInitiator{Kind: kindHuman}, true},
		{"human WITH a harp is malformed", ControlInitiator{Kind: kindHuman, Harp: "parent"}, false},
		{"agent naming itself", ControlInitiator{Kind: kindAgent, Harp: "parent"}, true},
		{"agent without a harp cannot be ownership-checked", ControlInitiator{Kind: kindAgent}, false},
		{"unspecified is not an initiator", ControlInitiator{Kind: kindUnspecified}, false},
		{"a kind this build does not know", ControlInitiator{Kind: agentcoordpb.ControlInitiatorKind(99)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
		})
	}
}

// steerRequest builds one plane-2 steer frame declaring what it needs.
func steerRequest(reqID, text string, required ...string) *agentcoordpb.CoordinatorRequest {
	return &agentcoordpb.CoordinatorRequest{
		RequestId:            reqID,
		RequiredCapabilities: required,
		Kind:                 &agentcoordpb.CoordinatorRequest_Steer{Steer: &agentcoordpb.SteerRequest{Text: text}},
	}
}

// synthChan installs a runChan for role with the given advertisement and
// returns it plus its writer pump, so a test can read the frames the
// coordinator queued without standing up a gRPC stream.
func synthChan(c *Coordinator, role string, caps ...string) *runChan {
	set := make(map[string]bool, len(caps))
	for _, cp := range caps {
		set[cp] = true
	}
	ch := &runChan{
		role:      role,
		id:        Identity{Harp: role},
		send:      make(chan *agentcoordpb.CoordinatorFrame, 16),
		caps:      set,
		completed: make(chan struct{}),
	}
	c.mu.Lock()
	c.chans[role] = ch
	c.mu.Unlock()
	return ch
}

// nextRequest reads the next queued down-request frame, or fails.
func nextRequest(t *testing.T, ch *runChan) *agentcoordpb.CoordinatorRequest {
	t.Helper()
	select {
	case frame := <-ch.send:
		req := frame.GetRequest()
		require.NotNil(t, req, "expected a CoordinatorRequest frame, got %T", frame.GetKind())
		return req
	case <-time.After(2 * time.Second):
		t.Fatal("no down-request frame was queued")
		return nil
	}
}

// TestRequestRun_ReissuesOnReattachAndAnswersOnce pins the reconnect contract:
// a request outstanding when the channel dies is reissued with its ORIGINAL
// request_id on the role's next attach, and the caller is answered exactly
// once no matter which channel the answer arrives on.
func TestRequestRun_ReissuesOnReattachAndAnswersOnce(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	first := synthChan(c, "child-a", CapPeerMessaging, CapSteer)

	type result struct {
		resp *agentcoordpb.AgentResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.requestRun(context.Background(), "child-a", steerRequest("ctl-1", "do the thing", CapSteer))
		done <- result{resp, err}
	}()

	sent := nextRequest(t, first)
	require.Equal(t, "ctl-1", sent.GetRequestId())

	// The channel dies and a FRESH one attaches, still advertising steer.
	second := synthChan(c, "child-a", CapPeerMessaging, CapSteer)
	c.redrainDownRequests(second)

	reissued := nextRequest(t, second)
	assert.Equal(t, "ctl-1", reissued.GetRequestId(),
		"the reissue must carry the ORIGINAL id — it is the runner's idempotency key, and a fresh id would mint a second execution")

	c.handleAgentResponse(second, &agentcoordpb.AgentResponse{
		RequestId: "ctl-1",
		Status:    okStatus(""),
		Kind: &agentcoordpb.AgentResponse_Steer{Steer: &agentcoordpb.SteerResult{
			Applied: agentcoordpb.SteerResult_APPLIED_NEXT_TURN,
		}},
	})

	select {
	case got := <-done:
		require.NoError(t, got.err)
		assert.Equal(t, agentcoordpb.SteerResult_APPLIED_NEXT_TURN, got.resp.GetSteer().GetApplied())
	case <-time.After(3 * time.Second):
		t.Fatal("the caller was never answered")
	}

	// A duplicate answer after the waiter is gone is inert, not a panic on a
	// closed/absent channel.
	c.handleAgentResponse(second, &agentcoordpb.AgentResponse{RequestId: "ctl-1", Status: okStatus("")})
}

// TestDrainSeam_TerminallyRejectsWhatTheNewRunCannotDo is the receiver-normative
// check at the moment of MAXIMUM STALENESS: a request enqueued while run N was
// attached meets run N+1's freshly advertised Hello. The rejection must be
// TERMINAL (never held for later — a request parked on a capability that may
// never return is a caller blocked forever), it must be TYPED so §5.6 can route
// on the cause, and it must happen BEFORE delivery so the action is left
// unconsumed for the fallback to take.
func TestDrainSeam_TerminallyRejectsWhatTheNewRunCannotDo(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	first := synthChan(c, "child-a", CapPeerMessaging, CapSteer)

	type result struct {
		resp *agentcoordpb.AgentResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := c.requestRun(context.Background(), "child-a", steerRequest("ctl-9", "steer me", CapSteer))
		done <- result{resp, err}
	}()
	require.Equal(t, "ctl-9", nextRequest(t, first).GetRequestId())

	// The run is replaced by one that hosts no engine — a crash-relaunch, or a
	// resume onto a shim-only runner. Capabilities ride each Hello, so this is
	// a genuinely different answer to "what can you do".
	second := synthChan(c, "child-a", CapPeerMessaging)
	c.redrainDownRequests(second)

	select {
	case got := <-done:
		require.Error(t, got.err)
		assert.Nil(t, got.resp)
		assert.ErrorIs(t, got.err, ErrCapabilityUnavailable,
			"the rejection must be typed on its CAUSE — fallback selection keys on capability-absence, not on a status code shared with other refusals")
		assert.Equal(t, codes.FailedPrecondition, status.Code(got.err))
		assert.Contains(t, got.err.Error(), CapSteer, "the refusal names the missing capability")
		assert.Contains(t, got.err.Error(), CapPeerMessaging, "the refusal names what the new run DID advertise")
	case <-time.After(3 * time.Second):
		t.Fatal("the drain seam neither delivered nor rejected — held-for-later is the liveness hazard this test exists for")
	}

	// Nothing was handed to the new run.
	select {
	case frame := <-second.send:
		t.Fatalf("a request the new run cannot execute was delivered anyway: %T", frame.GetKind())
	default:
	}
	// And it is no longer tracked, so a LATER reattach cannot resurrect it.
	c.mu.Lock()
	_, still := c.downTrack[reqKey{role: "child-a", reqID: "ctl-9"}]
	c.mu.Unlock()
	assert.False(t, still, "a terminally rejected request must leave the tracker")
}

// TestDrainSeam_IsSetContainmentNotAKindTable pins the seam's taxonomy-agnostic
// shape. A request declaring NOTHING passes regardless of the arm it carries —
// which is exactly why the declared field is not the boundary: the runner's own
// backstop keys on the ACTUAL kind, so under-declaring buys the sender nothing.
// Encoding the opposite here (a kind→capability table at the seam) would be a
// second table to keep in sync and a check that looks present while guarding
// something the receiver does not agree with.
func TestDrainSeam_IsSetContainmentNotAKindTable(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	first := synthChan(c, "child-a", CapPeerMessaging, CapSteer)
	done := make(chan error, 1)
	go func() {
		_, err := c.requestRun(context.Background(), "child-a", steerRequest("ctl-bare", "no declaration"))
		done <- err
	}()
	require.Equal(t, "ctl-bare", nextRequest(t, first).GetRequestId())

	second := synthChan(c, "child-a", CapPeerMessaging) // steer withdrawn
	c.redrainDownRequests(second)

	reissued := nextRequest(t, second)
	assert.Equal(t, "ctl-bare", reissued.GetRequestId(),
		"an undeclared request is delivered, and the RECEIVER refuses it on its actual kind — the seam holds no kind table")

	c.handleAgentResponse(second, &agentcoordpb.AgentResponse{
		RequestId: "ctl-bare",
		Status:    statusErr(codes.Unimplemented, "this run did not advertise \"steer\""),
	})
	select {
	case err := <-done:
		require.NoError(t, err, "the wire round-trip succeeded; the refusal is IN the response")
	case <-time.After(3 * time.Second):
		t.Fatal("the caller was never answered")
	}
}

// TestClearDownTrack_SettlesWaitersAtTheTerminal: a request whose target ended
// has no addressee and no prospect of one. It must fail NOW rather than burn
// its whole budget waiting on a run that is gone — and it must fail with a
// cause distinguishable from a capability refusal, because "the target died" is
// not "the target cannot do this".
func TestClearDownTrack_SettlesWaitersAtTheTerminal(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	ch := synthChan(c, "child-a", CapPeerMessaging, CapSteer)
	done := make(chan error, 1)
	go func() {
		_, err := c.requestRun(context.Background(), "child-a", steerRequest("ctl-end", "hello", CapSteer))
		done <- err
	}()
	require.Equal(t, "ctl-end", nextRequest(t, ch).GetRequestId())

	c.clearDownTrack("child-a")

	select {
	case err := <-done:
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrControlTargetEnded)
		assert.NotErrorIs(t, err, ErrCapabilityUnavailable,
			"an ended target is not a capability gap; conflating them sends the caller down the wrong fallback")
	case <-time.After(3 * time.Second):
		t.Fatal("the terminal left a control request waiting on a run that will never answer")
	}
}

// TestRequestRun_TimeoutSaysTheActionMayStillBeExecuting is desynchronisation 1,
// answered by the one-object collapse. Under the two-object design a timeout
// told the initiator FAILED while a durable mail copy of the body still
// delivered at the next boundary — success reported as failure. With the body
// riding the request there is no second copy: the only honest thing left to say
// is that the request may still be executing, which is what the error says, and
// a retry with the same id JOINS that dispatch rather than starting a second.
func TestRequestRun_TimeoutSaysTheActionMayStillBeExecuting(t *testing.T) {
	c := newTestCoordinatorAt(t, t.TempDir())
	t.Cleanup(c.Close)

	ch := synthChan(c, "child-a", CapPeerMessaging, CapSteer)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	_, err := c.requestRun(ctx, "child-a", steerRequest("ctl-slow", "no answer coming", CapSteer))
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "may still be executing")
	assert.NotErrorIs(t, err, ErrCapabilityUnavailable)
	require.Equal(t, "ctl-slow", nextRequest(t, ch).GetRequestId())

	// The tracker is clean, so a later reattach does not reissue a request
	// nobody is waiting on.
	c.mu.Lock()
	_, still := c.downTrack[reqKey{role: "child-a", reqID: "ctl-slow"}]
	c.mu.Unlock()
	assert.False(t, still)
}

// TestControlSteer_RefusesSelfTarget is the hazard-2 fence. In the owner-run
// topology the coordinating session's own credential is depth 1 with parent ==
// its own harp, so without this guard a session passes its own ownership check
// and can steer itself in a loop with no floor.
func TestControlSteer_RefusesSelfTarget(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.ControlSteer(context.Background(), ControlInitiator{Kind: kindAgent, Harp: out.Harp}, out.Harp, "steer myself")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot control itself")
	assert.Zero(t, c.pendingCount(out.Harp), "a refused steer must queue nothing anywhere")
}

// TestControlSteer_AgentInitiatorControlsOnlyItsOwnChildren pins guard 3's
// AGENT arm, and TestControlSteer_RefusesUnrecognisedInitiator pins that the
// switch is exhaustive rather than defaulting.
func TestControlSteer_AgentInitiatorControlsOnlyItsOwnChildren(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.ControlSteer(context.Background(), ControlInitiator{Kind: kindAgent, Harp: "some-other-parent"}, out.Harp, "not yours")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the parent")
	assert.Zero(t, c.pendingCount(out.Harp))
}

func TestControlSteer_RefusesUnrecognisedInitiator(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle }, conformanceWait, 10*time.Millisecond)

	_, err = c.ControlSteer(context.Background(), ControlInitiator{Kind: agentcoordpb.ControlInitiatorKind(99), Harp: "x"}, out.Harp, "who am I")
	require.Error(t, err)
	assert.Zero(t, c.pendingCount(out.Harp),
		"an initiator this build does not recognise must not fall into the human branch and deliver anyway")
}

// TestControlSteer_ReceiverRefusalRoutesToTheMailboxExactlyOnce covers
// desynchronisations 3 and 4 together, and it is the receiver-normative loop
// closed end to end: the coordinator's channel record claims the run offers
// steer, the RUNNER — whose Hello advertised only peer_messaging — refuses on
// the actual kind, and §5.6's fallback then carries the body. Exactly ONE copy
// must exist afterwards: under the two-object design a refusal-after-queue left
// a refused steer whose body still arrived as ordinary mail.
func TestControlSteer_ReceiverRefusalRoutesToTheMailboxExactlyOnce(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{turnGate: gate} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.chans[out.Harp] != nil
	}, conformanceWait, 10*time.Millisecond)

	// Claim a capability the runner never advertised (engineCaps is unset, so
	// its Hello named peer_messaging alone). The send-side check is EXPERIENCE;
	// the RECEIVER's refusal is the guarantee — this makes the two disagree on
	// purpose so it is the receiver's answer that decides.
	c.mu.Lock()
	c.chans[out.Harp].caps[CapSteer] = true
	c.mu.Unlock()

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "mid-turn note")
	require.NoError(t, err)
	assert.NotEmpty(t, outcome.Fallback, "a receiver refusal on a verb WITH a fallback routes, it does not fail")

	gate <- struct{}{}
	require.Eventually(t, func() bool {
		texts := sp.chat(0).recordedTexts()
		return len(texts) == 2 && texts[1] == frameCoordinatorDelivery(UserSender, KindSteer, "mid-turn note")
	}, conformanceWait, 10*time.Millisecond, "the fallback delivered the body")

	// One copy, not two: the plane-2 attempt queued no mail of its own, and the
	// runner parked nothing it will later hand over.
	assert.Zero(t, c.pendingCount(out.Harp), "no second copy of the body is left queued")
	msgs := recvKind(t, c, KindUserInjected, time.Second)
	require.Len(t, msgs, 1, "exactly one O3 mirror, on the fallback route too")
}

// TestControlSteer_PlaneTwoDeliversReminderAndBodyOnlyViaRecv is the stage's
// central proof, and it is three claims at once:
//
//   - the INJECTED TURN carries only the generated reminder — no instruction
//     bytes reach the turn stream, so a body that looks like a frame cannot
//     become one;
//   - the BODY arrives only through the runner-local agent_recv, tagged
//     MESSAGE_KIND_STEER and attributed to the user;
//   - the coordinator MAILBOX holds nothing (desynchronisation 5: under the
//     two-object design the body was ordinary pushable mail, so the turn sink
//     emitted a mail reminder ON TOP of the steer reminder — two injected turns
//     for one steer).
func TestControlSteer_PlaneTwoDeliversReminderAndBodyOnlyViaRecv(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.engineCaps = RunnerCapabilities(true) // what a production migrated child advertises
	sp.nextChat = func() *scriptedChat { return &scriptedChat{} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return rosterState(c, out.Harp) == StateIdle && c.runCapability(out.Harp, CapSteer) == nil
	}, conformanceWait, 10*time.Millisecond)

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "prefer sqlx")
	require.NoError(t, err)
	assert.Empty(t, outcome.Fallback, "an attached, advertising, migrated target rides plane 2")
	assert.Equal(t, agentcoordpb.SteerResult_APPLIED_IMMEDIATE, outcome.Applied,
		"an IDLE target starts a new turn now — APPLIED_IMMEDIATE is about latency, never about interrupting a running turn")

	// The injected-turn count GROWS while the body sits unpulled: the
	// turn-boundary re-announcer (enginehost_reannounce.go) injects an
	// UnpulledReminder at every boundary until the agent calls agent_recv,
	// and this fixture's scripted engine never pulls on its own — the pull
	// below is the test's, several statements away. So `== 2` names a state
	// the subject holds only between two consecutive boundaries, and each
	// reminder turn's own boundary produces the next one immediately. A
	// poller that misses that window sees 3 and the condition is false
	// FOREVER, which is exactly the 5s "Condition never satisfied" this test
	// kept producing under load (reproduced deterministically, 30/30, at
	// GOMAXPROCS=1: the re-announcer burned its whole budget — "announced 4
	// times over 0s" — before the poll ran once).
	//
	// Wait for the steer's own reminder to LAND instead. texts[1] is
	// deterministically THIS steer's SteerPendingReminder and nothing else:
	// the run was idle (asserted above, so turn 1's boundary is already
	// past), handleSteer parks the body and enqueues the reminder, and
	// planReannounce holds off entirely while a locally-originated turn is
	// still on the tag FIFO.
	require.Eventually(t, func() bool { return len(sp.chat(0).recordedTexts()) >= 2 }, conformanceWait, 10*time.Millisecond)
	texts := sp.chat(0).recordedTexts()
	injected := texts[1]
	assert.Equal(t, (&agentcoordpb.SteerPendingReminder{}).XmlLike(), injected)
	// "no instruction bytes reach the turn stream" is a claim about EVERY
	// turn this steer produced, not only the first one — re-announcements
	// included.
	for i, turn := range texts[1:] {
		assert.NotContains(t, turn, "prefer sqlx",
			"the instruction body must NEVER ride an injected turn (injected turn %d)", i+1)
	}

	home := sp.engineHome(0)
	require.NotNil(t, home)
	got, err := home.Recv(context.Background(), 2*time.Second)
	require.NoError(t, err, "the body must be waiting for the pull the reminder asks for")
	require.Len(t, got, 1)
	assert.Equal(t, "prefer sqlx", got[0].GetText())
	assert.Equal(t, agentcoordpb.MessageKind_MESSAGE_KIND_STEER, got[0].GetKind())
	assert.Equal(t, UserSender, got[0].GetFromAgentId())

	assert.Zero(t, c.pendingCount(out.Harp),
		"the plane-2 route queues NO coordinator mail — one steer is one object, so there is no second reminder and nothing to withdraw")
	require.Len(t, recvKind(t, c, KindUserInjected, time.Second), 1, "the O3 mirror is intact on the plane-2 route")
}

// TestControlSteer_PlaneTwoMidTurnReportsNextTurn pins the other half of
// `applied`: a target already in a turn cannot be interrupted, so the reminder
// queues to the next boundary and the acknowledgement SAYS SO. A caller that is
// told NEXT_TURN has learned something true; the mailbox route could only ever
// guess this from coordinator-side state.
func TestControlSteer_PlaneTwoMidTurnReportsNextTurn(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: "bypass", profiles: []string{"p1"}, viaStartRun: true},
	}, nil)
	sp.engineCaps = RunnerCapabilities(true)
	sp.nextChat = func() *scriptedChat { return &scriptedChat{turnGate: gate} }
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	// The engine has taken the briefing and is parked on the gate: the turn is
	// open and its input channel is not being read.
	require.Eventually(t, func() bool {
		sc := sp.chat(0)
		return sc != nil && len(sc.recordedTexts()) == 1 && c.runCapability(out.Harp, CapSteer) == nil
	}, conformanceWait, 10*time.Millisecond)

	outcome, err := c.ControlSteer(context.Background(), humanInitiator(), out.Harp, "change course")
	require.NoError(t, err)
	assert.Empty(t, outcome.Fallback)
	assert.Equal(t, agentcoordpb.SteerResult_APPLIED_NEXT_TURN, outcome.Applied)

	gate <- struct{}{} // release turn 1 so the reminder turn can start
	require.Eventually(t, func() bool { return len(sp.chat(0).recordedTexts()) == 2 }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, (&agentcoordpb.SteerPendingReminder{}).XmlLike(), sp.chat(0).recordedTexts()[1])
	gate <- struct{}{}
}

// TestInject_MapsPlaneTwoAcknowledgementOntoDeliveryVocabulary pins the
// translation the TUI depends on. APPLIED_IMMEDIATE means the target was IDLE
// and a new turn started now, which is DeliveryNewTurn; a REJECTED steer gets
// its OWN string, because the mailbox vocabulary had no way to say "it did not
// land" and printing a queue for a refusal is the silent-success failure mode.
func TestInject_MapsPlaneTwoAcknowledgementOntoDeliveryVocabulary(t *testing.T) {
	for _, tc := range []struct {
		applied agentcoordpb.SteerResult_Applied
		want    string
	}{
		{agentcoordpb.SteerResult_APPLIED_IMMEDIATE, DeliveryNewTurn},
		{agentcoordpb.SteerResult_APPLIED_NEXT_TURN, DeliveryQueued},
		{agentcoordpb.SteerResult_APPLIED_REJECTED, DeliveryRejected},
	} {
		assert.Equal(t, tc.want, steerAppliedToDelivery(tc.applied), "%v", tc.applied)
	}
	assert.NotEqual(t, DeliveryNewTurn, steerAppliedToDelivery(agentcoordpb.SteerResult_APPLIED_REJECTED),
		"a refusal must never read as a delivery")
}

// TestMissingCapabilities is the seam's predicate in isolation: pure set
// containment, empty declaration always passes, order stable for the message.
func TestMissingCapabilities(t *testing.T) {
	adv := map[string]bool{CapPeerMessaging: true, CapSteer: true}
	assert.Empty(t, missingCapabilities(nil, adv))
	assert.Empty(t, missingCapabilities([]string{CapSteer}, adv))
	assert.Empty(t, missingCapabilities([]string{""}, adv), "an empty entry is not a capability and is ignored")
	assert.Equal(t, []string{CapPause, CapQuestion}, missingCapabilities([]string{CapQuestion, CapPause}, adv))
	assert.Equal(t, []string{CapSteer}, missingCapabilities([]string{CapSteer}, map[string]bool{}))
}

// TestCapUnavailable_IsBothAStatusAndACause: the refusal has to satisfy two
// callers at once — a wire caller reading a gRPC code, and an in-process caller
// routing on the cause. Matching on the code alone cannot distinguish a
// capability gap from the other FAILED_PRECONDITIONs, and matching on prose is
// not a contract.
func TestCapUnavailable_IsBothAStatusAndACause(t *testing.T) {
	err := capUnavailable("run %q does not offer %q", "child-a", CapSteer)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.True(t, errors.Is(err, ErrCapabilityUnavailable))
	assert.False(t, errors.Is(err, ErrControlTargetEnded))
	assert.Contains(t, err.Error(), CapSteer)
}
