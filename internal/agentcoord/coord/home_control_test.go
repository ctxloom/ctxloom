package coord

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// dialHome stands up a REAL runner Home against c, attached as the session
// owner harp, advertising exactly caps. The end-to-end dial is the point: the
// backstop's whole job is to disagree with the coordinator when the two views
// have diverged, and only a real Hello puts a real advertisement on both sides.
func dialHome(t *testing.T, c *Coordinator, harp string, caps ...string) *Home {
	t.Helper()
	url, err := c.ReachURL("host")
	require.NoError(t, err)
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := NewHome(ctx, HomeConfig{
		URL: url, Token: token, Harness: "test", Version: "test",
		Capabilities: caps,
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.chans[harp] != nil
	}, 10*time.Second, 10*time.Millisecond, "the run channel must attach before a control request can be sent")
	// BOTH sides, because the handshake attaches them at different moments:
	// the coordinator registers c.chans[harp] when it READS the Hello, while
	// the runner sets h.stream only after it has read the HelloAck back. A
	// fixture that waits on the coordinator's half alone hands back a Home
	// whose stream is still nil, and every unbuffered runner->coordinator
	// send in that window is dropped ON PURPOSE (Home.trySend's nil-stream
	// arm) — silently, because fire-and-forget is the design. That is a
	// fixture defect, not a product one: it turns "the doorbell arrived" into
	// a race against the scheduler, lost whenever the box is busy.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.stream != nil
	}, 10*time.Second, 10*time.Millisecond, "the runner's own end of the run channel must be attached, or a send made now is dropped as 'run channel down'")
	return h
}

// TestHomeControl_BackstopKeysOnTheActualKindNotTheDeclaration is the constraint
// that makes the self-describing required_capabilities field SAFE to have.
//
// The request declares `peer_messaging` — which the run genuinely advertises,
// so the coordinator's drain seam passes it — while the arm it actually carries
// is a STEER. If the receiver trusted the declaration, under-declaring would be
// a way to smuggle an unadvertised kind past every check in the system. It does
// not: the backstop reads the oneof that is really there, so declared != actual
// fails SAFE.
func TestHomeControl_BackstopKeysOnTheActualKindNotTheDeclaration(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, "owner-harp", CapPeerMessaging)

	// An EXECUTOR is registered, so the only thing standing between the
	// smuggled steer and execution is the backstop. Without it this handler
	// runs — which is the whole hazard, not a message-wording detail.
	executed := make(chan struct{}, 1)
	h.SetRequestHandler(func(_ context.Context, _ *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
		executed <- struct{}{}
		return steerAck(agentcoordpb.SteerResult_APPLIED_IMMEDIATE)
	})

	resp, err := c.requestRun(context.Background(), "owner-harp",
		steerRequest("ctl-lie", "smuggled instruction", CapPeerMessaging))
	require.NoError(t, err, "the request round-tripped; the refusal is in the response")
	assert.Equal(t, codes.Unimplemented, codes.Code(resp.GetStatus().GetCode()))
	assert.Contains(t, resp.GetStatus().GetMessage(), CapSteer,
		"the refusal names the capability the ACTUAL kind needs, not the one the request claimed")
	assert.Contains(t, resp.GetStatus().GetMessage(), CapPeerMessaging,
		"and names what this run did advertise")
	assert.Nil(t, resp.GetSteer(), "a refused steer carries no result")

	select {
	case <-executed:
		t.Fatal("the executor ran for a kind this run never advertised — the declared field would then be a forgeable proxy for permission")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestHomeControl_UnrecognisedKindIsNotACapabilityGap keeps the two refusals
// typed apart. §5.6's fallback selection routes on capability-absence, so
// "I have never heard of this request" must NOT wear the same code as "I cannot
// do this" — otherwise a coordinator bug silently becomes a fallback.
func TestHomeControl_UnrecognisedKindIsNotACapabilityGap(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	dialHome(t, c, "owner-harp", RunnerCapabilities(true)...)

	resp, err := c.requestRun(context.Background(), "owner-harp", &agentcoordpb.CoordinatorRequest{
		RequestId: "ctl-unknown",
		Kind: &agentcoordpb.CoordinatorRequest_Custom{Custom: &agentcoordpb.CustomRequest{
			Name: "vendor/teleport",
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, codes.InvalidArgument, codes.Code(resp.GetStatus().GetCode()),
		"an unrecognised kind is a bug or a newer coordinator, never a capability gap")
	assert.NotEqual(t, codes.Unimplemented, codes.Code(resp.GetStatus().GetCode()))
}

// TestHomeControl_EnginelessRunnerRefusesEvenWhatItAdvertised: the advertisement
// is a claim, the handler is the ability. A runner that advertised the control
// kinds but bound no engine must say it hosts none rather than accept and drop
// the request — the silent no-op this project is prone to.
func TestHomeControl_EnginelessRunnerRefusesEvenWhatItAdvertised(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	dialHome(t, c, "owner-harp", RunnerCapabilities(true)...)

	resp, err := c.requestRun(context.Background(), "owner-harp", steerRequest("ctl-noengine", "hello", CapSteer))
	require.NoError(t, err)
	assert.Equal(t, codes.Unimplemented, codes.Code(resp.GetStatus().GetCode()))
	assert.Contains(t, resp.GetStatus().GetMessage(), "hosts no engine")
}

// TestHomeControl_ReissueReplaysTheCachedAnswerWithoutReExecuting is the
// responder-side idempotency the reconnect design depends on. A control request
// consumes a child TURN, so a second dispatch is not a wasted round trip — it is
// a turn nobody asked for, and for a steer it is the instruction delivered twice.
func TestHomeControl_ReissueReplaysTheCachedAnswerWithoutReExecuting(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, "owner-harp", RunnerCapabilities(true)...)

	var mu sync.Mutex
	calls := 0
	h.SetRequestHandler(func(_ context.Context, _ *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
		mu.Lock()
		calls++
		mu.Unlock()
		return steerAck(agentcoordpb.SteerResult_APPLIED_IMMEDIATE)
	})

	first, err := c.requestRun(context.Background(), "owner-harp", steerRequest("ctl-idem", "once", CapSteer))
	require.NoError(t, err)
	require.Equal(t, agentcoordpb.SteerResult_APPLIED_IMMEDIATE, first.GetSteer().GetApplied())

	// The SAME request id arrives again — what a reconnect reissue looks like
	// from the runner's side.
	second, err := c.requestRun(context.Background(), "owner-harp", steerRequest("ctl-idem", "once", CapSteer))
	require.NoError(t, err)
	assert.Equal(t, agentcoordpb.SteerResult_APPLIED_IMMEDIATE, second.GetSteer().GetApplied(),
		"the reissue is answered with the SAME cached response")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, calls, "the executor must run ONCE per request id, however many times the frame arrives")
}

// TestHomeControl_MissingRequestIdIsRefused: without a correlating id there is
// no idempotency key and no way to answer, so it is refused rather than
// executed into the void.
func TestHomeControl_MissingRequestIdIsRefused(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, "owner-harp", RunnerCapabilities(true)...)

	ran := make(chan struct{}, 1)
	h.SetRequestHandler(func(_ context.Context, _ *agentcoordpb.CoordinatorRequest) *agentcoordpb.AgentResponse {
		ran <- struct{}{}
		return steerAck(agentcoordpb.SteerResult_APPLIED_IMMEDIATE)
	})
	h.serveCoordinatorRequest(steerRequest("", "no id", CapSteer))

	select {
	case <-ran:
		t.Fatal("an uncorrelated request must not be executed")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestParkControlPayload_BypassesTheTurnSinkAndDedupes is desynchronisation 5,
// closed at its source. A control body is NOT ordinary pushable mail: routing it
// through deliverNotice would hand it to the engine as a full framed turn on top
// of the reminder the control verb already injected — two turns, and the body in
// the turn stream after all. It is also deduped on message id, which is what
// makes a reissued request park one copy rather than two.
func TestParkControlPayload_BypassesTheTurnSinkAndDedupes(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, "owner-harp", RunnerCapabilities(true)...)

	var mu sync.Mutex
	var sunk []string
	h.SetTurnSink(func(pm *agentcoordpb.PeerMessage) bool {
		mu.Lock()
		sunk = append(sunk, pm.GetMessageId())
		mu.Unlock()
		return true
	})

	body := &agentcoordpb.PeerMessage{
		MessageId:   steerBodyID("ctl-park"),
		FromAgentId: UserSender,
		Text:        "the instruction",
		Kind:        agentcoordpb.MessageKind_MESSAGE_KIND_STEER,
	}
	h.ParkControlPayload(body)
	h.ParkControlPayload(body) // the reissue

	got, err := h.Recv(context.Background(), 2*time.Second)
	require.NoError(t, err)
	require.Len(t, got, 1, "a reissued request parks ONE body, not two")
	assert.Equal(t, "the instruction", got[0].GetText())

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, sunk, "a control body must never reach the turn sink — the reminder is the turn, the body is the pull")
}

// TestPendingControlPayloads_TracksUnpulledBodies pins the state the
// turn-boundary re-announcer runs on (F10). "The agent never pulled the
// instruction" has to be a state something can OBSERVE — that it was not is
// exactly why losing a steer was silent — and it must clear on the one event
// that means the agent has it: the Recv that hands it over.
//
// This runs against the real Home rather than the engine host's fake, because a
// fake that says "still pending" while Home says "pulled" would make the
// re-announcer's whole test suite prove nothing.
func TestPendingControlPayloads_TracksUnpulledBodies(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	h := dialHome(t, c, "owner-harp", RunnerCapabilities(true)...)

	assert.Empty(t, h.PendingControlPayloads(), "nothing parked, nothing pending")

	before := time.Now()
	h.ParkControlPayload(&agentcoordpb.PeerMessage{
		MessageId: steerBodyID("ctl-first"), FromAgentId: UserSender, Text: "one",
		Kind: agentcoordpb.MessageKind_MESSAGE_KIND_STEER,
	})
	h.ParkControlPayload(&agentcoordpb.PeerMessage{
		MessageId: steerBodyID("ctl-second"), FromAgentId: UserSender, Text: "two",
		Kind: agentcoordpb.MessageKind_MESSAGE_KIND_STEER,
	})

	pending := h.PendingControlPayloads()
	require.Len(t, pending, 2)
	assert.Equal(t, steerBodyID("ctl-first"), pending[0].MessageID, "oldest first: the age the frame reports is the OLDEST wait")
	assert.Equal(t, steerBodyID("ctl-second"), pending[1].MessageID)
	assert.False(t, pending[0].ParkedAt.Before(before), "the park time is the runner's own clock, not a zero value")

	// The pull is the one event that ends the pending state.
	got, err := h.Recv(context.Background(), 2*time.Second)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Empty(t, h.PendingControlPayloads(), "a pulled body is not pending, and must never be re-announced")
}

// TestCapabilityForRequest_CoversEveryDeclaredArm: the receiver's kind→capability
// table is the ONE table that has to be right, because the receiver's refusal is
// the guarantee. An arm with no entry returns "" and is refused as unrecognised
// — never defaulted into an executable capability.
func TestCapabilityForRequest_CoversEveryDeclaredArm(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *agentcoordpb.CoordinatorRequest
		want string
	}{
		{"steer", &agentcoordpb.CoordinatorRequest{Kind: &agentcoordpb.CoordinatorRequest_Steer{Steer: &agentcoordpb.SteerRequest{}}}, CapSteer},
		{"question", &agentcoordpb.CoordinatorRequest{Kind: &agentcoordpb.CoordinatorRequest_Question{Question: &agentcoordpb.QuestionRequest{}}}, CapQuestion},
		{"summarize", &agentcoordpb.CoordinatorRequest{Kind: &agentcoordpb.CoordinatorRequest_Summarize{Summarize: &agentcoordpb.SummarizeRequest{}}}, CapSummarize},
		{"pause", &agentcoordpb.CoordinatorRequest{Kind: &agentcoordpb.CoordinatorRequest_Pause{Pause: &agentcoordpb.PauseRequest{}}}, CapPause},
		{"resume", &agentcoordpb.CoordinatorRequest{Kind: &agentcoordpb.CoordinatorRequest_Resume{Resume: &agentcoordpb.ResumeRequest{}}}, CapResume},
		{"no arm at all", &agentcoordpb.CoordinatorRequest{}, ""},
		{"custom", &agentcoordpb.CoordinatorRequest{Kind: &agentcoordpb.CoordinatorRequest_Custom{Custom: &agentcoordpb.CustomRequest{}}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, capabilityForRequest(tc.req))
		})
	}
	// And every control capability the runner can advertise has an arm that
	// maps back onto it: a cap with no arm is a request that would be accepted
	// by the backstop and then answered "no executor" — a promise nothing keeps.
	mapped := map[string]bool{}
	for _, req := range []*agentcoordpb.CoordinatorRequest{
		{Kind: &agentcoordpb.CoordinatorRequest_Steer{Steer: &agentcoordpb.SteerRequest{}}},
		{Kind: &agentcoordpb.CoordinatorRequest_Question{Question: &agentcoordpb.QuestionRequest{}}},
		{Kind: &agentcoordpb.CoordinatorRequest_Summarize{Summarize: &agentcoordpb.SummarizeRequest{}}},
		{Kind: &agentcoordpb.CoordinatorRequest_Pause{Pause: &agentcoordpb.PauseRequest{}}},
		{Kind: &agentcoordpb.CoordinatorRequest_Resume{Resume: &agentcoordpb.ResumeRequest{}}},
	} {
		mapped[capabilityForRequest(req)] = true
	}
	for _, cp := range ControlCapabilities() {
		assert.True(t, mapped[cp], "capability %q has no request arm", cp)
	}
}
