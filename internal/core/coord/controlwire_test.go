package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// The ControlRun wire pair (controlwire.go): a coordinating session's
// agent_steer/agent_ask/agent_summarize/agent_pause/agent_resume arrive as
// one plane-2 request kind and are answered by the SAME verbs the human's
// viewer path calls. What these tests pin is the transport's contract — the
// initiator is the requester's credential, ownership is the verb's guard, and
// each verb's EFFECT is observable on the child — not the verbs themselves,
// which spoolcontrol_test.go covers at the in-process seam.

// controlFrame wraps one verb message as the ControlRun request it rides in;
// nil is a ControlRun with no arm set.
func controlFrame(t *testing.T, verb any) *agentcoordpb.AgentRequest {
	t.Helper()
	req := &agentcoordpb.ControlRun{}
	switch v := verb.(type) {
	case *agentcoordpb.ControlSteer:
		req.Verb = &agentcoordpb.ControlRun_Steer{Steer: v}
	case *agentcoordpb.ControlQuestion:
		req.Verb = &agentcoordpb.ControlRun_Question{Question: v}
	case *agentcoordpb.ControlSummarize:
		req.Verb = &agentcoordpb.ControlRun_Summarize{Summarize: v}
	case *agentcoordpb.ControlPause:
		req.Verb = &agentcoordpb.ControlRun_Pause{Pause: v}
	case *agentcoordpb.ControlResume:
		req.Verb = &agentcoordpb.ControlRun_Resume{Resume: v}
	case nil:
		// no verb set
	default:
		t.Fatalf("controlFrame: %T is not a ControlRun arm", verb)
	}
	return &agentcoordpb.AgentRequest{Kind: &agentcoordpb.AgentRequest_ControlRun{ControlRun: req}}
}

// controlRun sends one ControlRun on home and returns the response.
func controlRun(t *testing.T, home *Home, verb any) *agentcoordpb.CoordinatorResponse {
	t.Helper()
	resp, err := home.Request(context.Background(), controlFrame(t, verb))
	require.NoError(t, err)
	return resp
}

// controlRunAsync is controlRun for a call that BLOCKS on the child (an
// ask): the wire call runs on its own goroutine and the response — or the
// transport error — lands on the returned channel, so no assertion runs off
// the test goroutine.
func controlRunAsync(t *testing.T, home *Home, verb any) <-chan *agentcoordpb.CoordinatorResponse {
	t.Helper()
	frame := controlFrame(t, verb)
	out := make(chan *agentcoordpb.CoordinatorResponse, 1)
	go func() {
		resp, err := home.Request(context.Background(), frame)
		if err != nil {
			resp = &agentcoordpb.CoordinatorResponse{Status: statusErr(codes.Unavailable, "transport: "+err.Error())}
		}
		out <- resp
	}()
	return out
}

// TestControlRun_ChildSteersItsOwnGrandchild is the row's whole point: the
// AGENT initiator. A depth-1 child that spawned a grandchild over plane 2
// steers it over plane 2, and the grandchild's ENGINE is driven with the
// instruction under the steer kind — the same effect a human steer has, via
// the same verb, reached by an agent instead.
func TestControlRun_ChildSteersItsOwnGrandchild(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		Depth:              2, // a depth-1 child may coordinate a depth-2 grandchild
		OwnerHarp:          ownerIdentity().Harp,
		SpoolSweepInterval: 0,
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	child, childH := awaitCutoverChild(t, c, sp, "delegate this")

	input, err := structpb.NewStruct(map[string]any{"prompt": "go deeper"})
	require.NoError(t, err)
	spawnResp, err := childH.Request(context.Background(), &agentcoordpb.AgentRequest{
		Kind: &agentcoordpb.AgentRequest_SpawnAgent{SpawnAgent: &agentcoordpb.SpawnAgentRequest{Role: "worker", Input: input}},
	})
	require.NoError(t, err)
	require.EqualValues(t, codes.OK, spawnResp.GetStatus().GetCode(), spawnResp.GetStatus().GetMessage())
	grandchild := spawnResp.GetSpawnAgent().GetChildAgentId()
	require.NoError(t, c.awaitChildUp(context.Background(), grandchild))
	require.Eventually(t, func() bool { return sp.engineHome(1) != nil }, conformanceWait, 10*time.Millisecond)

	resp := controlRun(t, childH, &agentcoordpb.ControlSteer{Harp: grandchild, Text: "rebase before you continue"})
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	steered := resp.GetControlRun().GetSteer()
	require.NotNil(t, steered, "the steer arm answers the steer arm")
	assert.NotEmpty(t, steered.GetMessageId(), "the withdraw handle rides back to the initiator")
	assert.NotEmpty(t, steered.GetDelivery())

	// THE EFFECT: the grandchild's engine (index 1) is driven with the
	// instruction, and the child's own engine (index 0) is not.
	turns := awaitChatText(t, sp, 1, "rebase before you continue")
	var delivered string
	for _, turn := range turns {
		if strings.Contains(turn, "rebase before you continue") {
			delivered = turn
		}
	}
	assert.Contains(t, delivered, "kind="+KindSteer)
	assert.Contains(t, delivered, child.Harp, "the provenance header names the CHILD as the steer's author, not the human")
	assert.Zero(t, countChatText(sp, 0, "rebase before you continue"), "the instruction must reach the target alone")

	// And the instruction is the durable file the handle names.
	consumed := awaitSpoolCount(t, grandchild, spool.DirInConsumed, 1, "after the grandchild took the steer")
	assert.Equal(t, steered.GetMessageId(), consumed[0].Message.OriginID)
}

// TestControlRun_SteerFromTheOwnerReachesTheChild: the session owner's own
// runner rides the same pair (a `ctxloom run` session's agent_steer goes
// through its owner Home, not the stdio path), and the delivery mode it is
// told is the one the child actually got.
//
// The mode is the state the delivery OBSERVED AT THE WRITE. The interleaving
// that used to redden this under load is FORCED here: the child takes the
// steer and starts its turn the instant the file lands (afterMailWritten, the
// seam between the write and the disposition), so a disposition read AFTER
// the write would see an executing child and answer "queued" for a steer
// that woke an idle one.
func TestControlRun_SteerFromTheOwnerReachesTheChild(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChildIdle(t, c, sp, "first task")
	owner := ownerHome(t, c)

	c.afterMailWritten = func(to string) {
		if to == out.Harp {
			c.onTurnStarted(out.Harp) // the child took it before anyone looked
		}
	}

	resp := controlRun(t, owner, &agentcoordpb.ControlSteer{Harp: out.Harp, Text: "stop and rebase first"})
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.Equal(t, DeliveryNewTurn, resp.GetControlRun().GetSteer().GetDelivery(),
		"an idle child is woken into a new turn, and the wire must say so")
	awaitChatText(t, sp, 0, "stop and rebase first")
}

// TestControlRun_QuestionIsAnsweredOverTheWire: the ask blocks on the wire
// until the child's own correlated reply, and the answer — text and
// structured companion — rides back in the question arm.
func TestControlRun_QuestionIsAnsweredOverTheWire(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, childH := awaitCutoverChild(t, c, sp, "first task")
	owner := ownerHome(t, c)

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }

	responses := controlRunAsync(t, owner, &agentcoordpb.ControlQuestion{Harp: out.Harp, Text: "why sqlx over diesel?"})

	var askID string
	select {
	case askID = <-askIDs:
	case <-time.After(conformanceWait):
		t.Fatal("the ask was never published")
	}
	awaitChatText(t, sp, 0, "why sqlx over diesel?")
	structured, err := structpb.NewStruct(map[string]any{"confidence": "high"})
	require.NoError(t, err)
	answerAsk(t, childH, askID, "compile-time checked queries", structured)

	select {
	case resp := <-responses:
		require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		ans := resp.GetControlRun().GetQuestion()
		require.NotNil(t, ans, "the question arm answers the question arm")
		assert.Equal(t, askID, ans.GetAskId())
		assert.Equal(t, out.Harp, ans.GetFrom())
		assert.Equal(t, "compile-time checked queries", ans.GetText())
		assert.Equal(t, "high", ans.GetStructured().GetFields()["confidence"].GetStringValue(),
			"the structured companion must survive the file AND the wire")
	case <-time.After(conformanceWait):
		t.Fatal("the wire ask never resolved")
	}
}

// TestControlRun_SummarizeCarriesItsKindAndAnswersInItsArm: same mechanism
// as the question, and the child can tell the two apart — the summarize kind
// renders into the turn it is prompted with — and so can the caller: the
// answer arrives in the summarize arm, not the question arm.
func TestControlRun_SummarizeCarriesItsKindAndAnswersInItsArm(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, childH := awaitCutoverChild(t, c, sp, "first task")
	owner := ownerHome(t, c)

	askIDs := make(chan string, 1)
	c.onAskPublished = func(id string) { askIDs <- id }

	responses := controlRunAsync(t, owner, &agentcoordpb.ControlSummarize{Harp: out.Harp, Focus: "what is blocking you"})

	var askID string
	select {
	case askID = <-askIDs:
	case <-time.After(conformanceWait):
		t.Fatal("the ask was never published")
	}
	turns := awaitChatText(t, sp, 0, "what is blocking you")
	var delivered string
	for _, turn := range turns {
		if strings.Contains(turn, "what is blocking you") {
			delivered = turn
		}
	}
	assert.Contains(t, delivered, "kind="+KindSummarize)
	answerAsk(t, childH, askID, "nothing; two files left", nil)

	select {
	case resp := <-responses:
		require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
		assert.Nil(t, resp.GetControlRun().GetQuestion(), "a summary is not a question's answer")
		assert.Equal(t, "nothing; two files left", resp.GetControlRun().GetSummarize().GetText())
	case <-time.After(conformanceWait):
		t.Fatal("the wire ask never resolved")
	}
}

// TestControlRun_PauseHoldsTurnsAndResumeReleases is Q6 on the wire: the
// caller told "paused" must be looking at a child that takes no new turn, and
// "resumed" at one that then does. The newly_* results are the runner's own
// word, so a second pause says it found the gate already there.
func TestControlRun_PauseHoldsTurnsAndResumeReleases(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")
	owner := ownerHome(t, c)

	resp := controlRun(t, owner, &agentcoordpb.ControlPause{Harp: out.Harp, Reason: "reviewing its first batch"})
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.True(t, resp.GetControlRun().GetPause().GetNewlyPaused(), "the first pause installed the gate")

	_, _, _, err := c.peerSend(ownerIdentity(), out.Harp, KindMessage, "work item while paused", nil, "")
	require.NoError(t, err)
	require.Never(t, func() bool { return countChatText(sp, 0, "work item while paused") > 0 },
		750*time.Millisecond, 10*time.Millisecond, "a paused run must take no new turn")

	resp = controlRun(t, owner, &agentcoordpb.ControlPause{Harp: out.Harp})
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode())
	assert.False(t, resp.GetControlRun().GetPause().GetNewlyPaused(), "a second pause finds the gate and must say so")

	resp = controlRun(t, owner, &agentcoordpb.ControlResume{Harp: out.Harp})
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	assert.True(t, resp.GetControlRun().GetResume().GetNewlyResumed())
	awaitChatText(t, sp, 0, "work item while paused")

	resp = controlRun(t, owner, &agentcoordpb.ControlResume{Harp: out.Harp})
	require.EqualValues(t, codes.OK, resp.GetStatus().GetCode())
	assert.False(t, resp.GetControlRun().GetResume().GetNewlyResumed(), "resuming a running child found no gate")
}

// TestControlRun_RefusesWhatIsNotTheCallersChild: the ownership guard is the
// verb's, reached with the REQUESTER'S credential as the initiator — a child
// can control neither itself nor a sibling, and nothing is delivered on a
// refusal. PERMISSION_DENIED is the typed cause (ErrControlRefused), and an
// unknown target is NOT_FOUND, so a caller can tell "not yours" from "no such
// child" without reading prose.
func TestControlRun_RefusesWhatIsNotTheCallersChild(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	a, aHome := awaitCutoverChild(t, c, sp, "first task")
	b, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "second task", "", "")
	require.NoError(t, err)
	require.NoError(t, c.awaitChildUp(context.Background(), b.Harp))

	for _, tc := range []struct {
		name string
		harp string
		code codes.Code
	}{
		{"itself", a.Harp, codes.PermissionDenied},
		{"a sibling", b.Harp, codes.PermissionDenied},
		{"a harp no run answers to", "no-such-harp", codes.NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := controlRun(t, aHome, &agentcoordpb.ControlSteer{Harp: tc.harp, Text: "not yours to steer"})
			assert.EqualValues(t, tc.code, resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
			assert.Nil(t, resp.GetControlRun(), "a refusal carries no result arm")
		})
	}
	assert.Zero(t, countChatText(sp, 0, "not yours to steer"))
	assert.Zero(t, countChatText(sp, 1, "not yours to steer"))
	assert.Empty(t, spoolEntries(t, b.Harp, spool.DirIn), "a refused steer must queue nothing for the sibling")
}

// TestControlRun_ArgumentEdge: the wire edge names the missing argument as
// INVALID_ARGUMENT before any verb runs — an empty harp would otherwise be
// reported as "not a child", which is true and useless.
func TestControlRun_ArgumentEdge(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	sp := cutoverSpawner(0)
	c := newCutoverCoordinator(t, sp, 0)
	out, _ := awaitCutoverChild(t, c, sp, "first task")
	owner := ownerHome(t, c)

	for _, tc := range []struct {
		name string
		verb any
		want string
	}{
		{"no verb", nil, "no verb set"},
		{"steer without a harp", &agentcoordpb.ControlSteer{Text: "x"}, "harp is required"},
		{"steer without text", &agentcoordpb.ControlSteer{Harp: out.Harp}, "text is required"},
		{"question without text", &agentcoordpb.ControlQuestion{Harp: out.Harp}, "text is required"},
		{"summarize without a focus", &agentcoordpb.ControlSummarize{Harp: out.Harp}, "focus is required"},
		{"pause without a harp", &agentcoordpb.ControlPause{Reason: "why"}, "harp is required"},
		{"resume without a harp", &agentcoordpb.ControlResume{}, "harp is required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := controlRun(t, owner, tc.verb)
			assert.EqualValues(t, codes.InvalidArgument, resp.GetStatus().GetCode())
			assert.Contains(t, resp.GetStatus().GetMessage(), tc.want)
		})
	}
	assert.Empty(t, spoolEntries(t, out.Harp, spool.DirIn), "a refused argument must deliver nothing")
}

// TestControlStatus_MapsTypedCausesOnly pins the code each typed cause earns,
// and that an untyped error is INTERNAL rather than guessed at from prose.
func TestControlStatus_MapsTypedCausesOnly(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{ErrControlRefused, codes.PermissionDenied},
		{ErrNotInjectable, codes.NotFound},
		{ErrCapabilityUnavailable, codes.FailedPrecondition},
		{ErrAskUnavailable, codes.FailedPrecondition},
		{ErrAskTimeout, codes.DeadlineExceeded},
		{errors.New("permission denied: something that only SAYS so"), codes.Internal},
	} {
		st := controlStatus("agent_x", errors.Join(tc.err))
		assert.EqualValues(t, tc.code, st.GetCode(), tc.err.Error())
		assert.True(t, strings.HasPrefix(st.GetMessage(), "agent_x: "), "the tool names itself in the message")
	}
}
