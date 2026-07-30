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

// TestHandleControl_RefusesBeforeStartRun: with no run started there is no turn
// stream to enqueue onto. Answering FAILED_PRECONDITION is the honest outcome;
// accepting and dropping it is the silent no-op.
func TestHandleControl_RefusesBeforeStartRun(t *testing.T) {
	home := &fakeEngineHome{}
	eh := NewEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)

	resp := home.control(context.Background(), steerRequest("ctl-early", "too soon", CapSteer))
	require.NotNil(t, resp, "BindHome must have registered the executor on the Home seam")
	assert.Equal(t, codes.FailedPrecondition, codes.Code(resp.GetStatus().GetCode()))
	assert.Empty(t, home.parkedBodies(), "nothing may be parked for a run that never started")
}

// TestHandleControl_SteerParksTheBodyBeforeInjectingTheReminder pins the order
// the whole reminder+pull convention rests on. If the reminder went first, an
// agent that calls agent_recv the instant it sees the reminder finds nothing —
// and an instruction that reads as a phantom is worse than one that never
// arrived.
func TestHandleControl_SteerParksTheBodyBeforeInjectingTheReminder(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	require.Equal(t, int32(0), eh.Handle(&agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")},
	}).GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	meta, err := structpb.NewStruct(map[string]any{"from": UserSender})
	require.NoError(t, err)
	req := &agentcoordpb.CoordinatorRequest{
		RequestId:            "ctl-order",
		RequiredCapabilities: []string{CapSteer},
		Kind: &agentcoordpb.CoordinatorRequest_Steer{Steer: &agentcoordpb.SteerRequest{
			Text: "prefer sqlx", Metadata: meta,
		}},
	}
	resp := home.control(context.Background(), req)
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())

	// The body was parked — once, tagged, attributed, and correlated.
	parked := home.parkedBodies()
	require.Len(t, parked, 1)
	assert.Equal(t, steerBodyID("ctl-order"), parked[0].GetMessageId())
	assert.Equal(t, "prefer sqlx", parked[0].GetText())
	assert.Equal(t, agentcoordpb.MessageKind_MESSAGE_KIND_STEER, parked[0].GetKind())
	assert.Equal(t, UserSender, parked[0].GetFromAgentId())
	assert.Equal(t, "ctl-order", parked[0].GetStructured().GetFields()["request_id"].GetStringValue())

	// The injected turn carries the reminder and NOTHING else.
	//
	// `>= 2`, not `== 2`: this scripted engine completes every turn the instant
	// it arrives and never pulls the parked body, so the turn-boundary
	// re-announcer legitimately follows up (enginehost_reannounce.go). What
	// this test pins is the SECOND turn's content; a later re-announcement is
	// another test's subject, and an equality here would be a stopwatch.
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) >= 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, (&agentcoordpb.SteerPendingReminder{}).XmlLike(), sc.recordedTexts()[1])
}

// TestHandleControl_SteerRefusesEmptyText: empty input must FAIL, not succeed
// having enqueued a reminder for a body that says nothing.
func TestHandleControl_SteerRefusesEmptyText(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	require.Equal(t, int32(0), eh.Handle(&agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")},
	}).GetStatus().GetCode())
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	resp := home.control(context.Background(), steerRequest("ctl-empty", "", CapSteer))
	assert.Equal(t, codes.InvalidArgument, codes.Code(resp.GetStatus().GetCode()))
	assert.Empty(t, home.parkedBodies())
	assert.Len(t, sc.recordedTexts(), 1, "no turn may be injected for an empty steer")
}

// TestEnqueueTurn_KeepsTheTagFifoInSendOrder pins the turn-attribution
// substrate. Turn order equals send order only because enqueueTurn serializes
// the sends; a FIFO that can be pushed out of band is a FIFO that will
// eventually attribute a control turn's output to the wrong request, which is
// how a question returns someone else's answer.
func TestEnqueueTurn_KeepsTheTagFifoInSendOrder(t *testing.T) {
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	require.Equal(t, int32(0), eh.Handle(&agentcoordpb.RunnerRequest{
		Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")},
	}).GetStatus().GetCode())

	// The briefing's tag is pushed synchronously at startRun, so it is always
	// first regardless of when its send lands.
	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 1 }, 5*time.Second, 10*time.Millisecond)

	ctx := context.Background()
	require.NoError(t, eh.enqueueTurn(ctx, turnTag{kind: "steer", reqID: "a"}, "first"))
	require.NoError(t, eh.enqueueTurn(ctx, turnTag{}, "second"))
	require.NoError(t, eh.enqueueTurn(ctx, turnTag{kind: "steer", reqID: "c"}, "third"))

	require.Eventually(t, func() bool { return len(sc.recordedTexts()) == 4 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"CTX\n\ndo the thing", "first", "second", "third"}, sc.recordedTexts())

	// Every enqueued turn was popped by the adapt loop, in order, leaving the
	// FIFO empty rather than drifting one entry per turn.
	require.Eventually(t, func() bool {
		eh.mu.Lock()
		defer eh.mu.Unlock()
		return len(eh.pendingTags) == 0
	}, 5*time.Second, 10*time.Millisecond, "the FIFO must drain with the turns, not accumulate")
}

// TestEnqueueTurn_RefusesBeforeAnyRunStarted: there is no turn stream yet, and
// silently returning nil would let a control verb report success for a turn
// nothing will ever take.
func TestEnqueueTurn_RefusesBeforeAnyRunStarted(t *testing.T) {
	eh := NewEngineHost(context.Background(), &scriptedChat{}, "claude-code", "run-1")
	t.Cleanup(eh.Close)
	err := eh.enqueueTurn(context.Background(), turnTag{}, "nowhere to go")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no run has started")
}
