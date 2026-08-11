package coord

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// The reconnect race (fix/approval-reconnect-race): an approval relay to the
// parent is a minutes-long window (human response time). If the coordinator↔
// runner RunChannel reconnects INSIDE that window, the runner reissues the
// still-pending ApprovalRequest with the SAME request_id (home.go). The bug:
// reqCache/inflight lived on the per-connection runChan and reset to empty on
// every dial (runchannel.go), so the reissue hit an EMPTY cache on the fresh
// channel and dispatched serveApproval a SECOND time — a second relay mail, a
// second ladder walk. The two race: the human's ACCEPT answers one, the other
// never gets a reply, times out at the bottom of the ladder, and DECLINEs on
// the now-live channel. A human ACCEPT silently became a DECLINE. This is the
// one place ctxloom must never guess.
//
// This test drives the RunChannel as a RAW bidi gRPC stream so it fully owns
// the request_id and the reconnect: send an approval, let the relay begin,
// KILL the stream and REDIAL before the parent replies, reissue the SAME
// request_id, then have the parent ACCEPT — and assert (a) exactly ONE relay
// mail was ever queued for the item and (b) the single ACCEPT resolves the
// request as ACCEPTED on the currently-live channel.

// approvalReconnectSpawner is a fakeSpawner whose StartEngine attaches ONLY the
// lifecycle RunnerChannel (answering StartRun OK so AgentRun completes) and
// deliberately does NOT open a RunChannel/EngineHost — leaving the run's
// RunChannel for the test to drive raw. It captures the per-spawn runner env
// (the coordinator reach-back trio) so the test can dial with the child's own
// credential.
type approvalReconnectSpawner struct {
	*fakeSpawner
	mu    sync.Mutex
	links []*RunnerLink
	envCh chan map[string]string
}

func newApprovalReconnectSpawner(ladder Ladder) *approvalReconnectSpawner {
	return &approvalReconnectSpawner{
		fakeSpawner: newFakeSpawner(map[string]fakeAgent{
			"worker": {perm: "plan", runtime: "container", profiles: []string{"p1"}, viaStartRun: true, ladder: ladder},
		}, nil),
		envCh: make(chan map[string]string, 1),
	}
}

func (s *approvalReconnectSpawner) StartEngine(ctx context.Context, plan *SpawnPlan, env, runnerEnv map[string]string) (*EngineSpawn, error) {
	// Answer the coordinator's StartRun OK so runChildViaStartRun completes,
	// but never touch the RunChannel — the test is the runner's approval side.
	handler := func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
		if req.GetStartRun() != nil {
			return &agentcoordpb.RunnerResponse{
				Status: okStatus(""),
				Kind:   &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{}},
			}
		}
		return &agentcoordpb.RunnerResponse{Status: okStatus("")}
	}
	link, err := DialRunner(ctx, runnerEnv[EnvCoordURL], runnerEnv[EnvCoordCred], runnerEnv[EnvRunID], plan.Backend, "test", handler)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.links = append(s.links, link)
	s.mu.Unlock()
	select {
	case s.envCh <- runnerEnv:
	default:
	}
	// Kill can be invoked from more than one teardown path (terminateRun and
	// the coordinator's own Close-time runner sever); guard the single link
	// Shutdown so the test double never races itself.
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { link.Shutdown(0, "") }) }
	return &EngineSpawn{WorkDir: "/work", Env: env, Model: "test-model", Kill: kill}, nil
}

// rawRunChannel is a hand-driven client side of a RunChannel bidi stream: the
// test speaks the wire directly (Hello/HelloAck, approval requests, response
// reads) so it can reconnect at will without a Home in the loop.
type rawRunChannel struct {
	conn   *grpc.ClientConn
	cancel context.CancelFunc
	stream grpc.BidiStreamingClient[agentcoordpb.AgentFrame, agentcoordpb.CoordinatorFrame]
	frames chan *agentcoordpb.CoordinatorFrame
}

func dialRawRunChannel(t *testing.T, url, token, runID string) *rawRunChannel {
	t.Helper()
	target, err := grpcTarget(url)
	require.NoError(t, err)
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds(token)))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := agentcoordpb.NewCoordinatorServiceClient(conn).RunChannel(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Hello{Hello: &agentcoordpb.Hello{
		RunId: runID, ProtocolVersion: 1, Capabilities: []string{"peer_messaging"},
	}}}))
	first, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, first.GetHelloAck().GetAccepted(), "raw run channel Hello must be accepted")

	r := &rawRunChannel{conn: conn, cancel: cancel, stream: stream, frames: make(chan *agentcoordpb.CoordinatorFrame, 32)}
	go func() {
		for {
			f, rerr := stream.Recv()
			if rerr != nil {
				return
			}
			select {
			case r.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	return r
}

func (r *rawRunChannel) sendApproval(t *testing.T, reqID, itemID string) {
	t.Helper()
	require.NoError(t, r.stream.Send(&agentcoordpb.AgentFrame{Kind: &agentcoordpb.AgentFrame_Request{Request: &agentcoordpb.AgentRequest{
		RequestId: reqID,
		Kind: &agentcoordpb.AgentRequest_Approval{Approval: &agentcoordpb.ApprovalRequest{
			Kind:   agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION,
			Title:  "bash",
			ItemId: itemID,
		}},
	}}}))
}

// awaitApprovalDecision reads frames until a CoordinatorResponse for reqID
// arrives (skipping acks/notices), returning its ApprovalDecision.
func (r *rawRunChannel) awaitApprovalDecision(t *testing.T, reqID string, wait time.Duration) *agentcoordpb.ApprovalDecision {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case f := <-r.frames:
			if resp := f.GetResponse(); resp != nil && resp.GetRequestId() == reqID {
				return resp.GetApproval()
			}
		case <-deadline:
			t.Fatalf("no approval response for %q on the live channel within %s", reqID, wait)
			return nil
		}
	}
}

func (r *rawRunChannel) close() {
	r.cancel()
	_ = r.conn.Close()
}

// TestApproval_ReconnectDoesNotDuplicateOrFlipDecision is the reconnect-race
// gate. A relay is outstanding when the RunChannel reconnects; the runner
// reissues the same request_id; the coordinator must NOT dispatch a second
// approval, and the human's ACCEPT must resolve as ACCEPTED on the live
// channel — not silently become a DECLINE.
func TestApproval_ReconnectDoesNotDuplicateOrFlipDecision(t *testing.T) {
	resetStrictness(t)

	// A single relay_to_role rung with a bounded timeout: on the BUGGY path the
	// second (duplicate) ladder walk has nothing to answer it and falls to the
	// ladder bottom when this elapses — the flip the incident recorded as
	// ACCEPT→DECLINE, which today's bottom would express as a CANCEL (wiry-judge:
	// nobody decided). Either way the human's ACCEPT is lost, which is what this
	// gate exists to prevent. On the fixed
	// path there is only ONE walk and the parent's reply resolves it at once,
	// well inside this window, so the timeout is never paid.
	ladder := Ladder{{Action: ActionRelayToRole, Role: ParentAddress, Timeout: 5 * time.Second}}
	sp := newApprovalReconnectSpawner(ladder)
	c := newTestCoordinator(t, sp, nil)

	// Count every relay mail the ladder queues (one call to relayApproval == one
	// hook fire == one queued approval_request mail). The FIRST msg id is the
	// correlation the parent answers.
	var (
		relayMu   sync.Mutex
		relayMsgs []string
	)
	c.onApprovalMailQueued = func(msgID string) {
		relayMu.Lock()
		relayMsgs = append(relayMsgs, msgID)
		relayMu.Unlock()
	}
	relayCount := func() int {
		relayMu.Lock()
		defer relayMu.Unlock()
		return len(relayMsgs)
	}
	firstRelayMsg := func() string {
		relayMu.Lock()
		defer relayMu.Unlock()
		return relayMsgs[0]
	}

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "run a command", "", "")
	require.NoError(t, err)

	// StartEngine has attached the lifecycle runner and handed us the child's
	// reach-back trio.
	var env map[string]string
	select {
	case env = <-sp.envCh:
	case <-time.After(conformanceWait):
		t.Fatal("child runner never started")
	}

	// Channel #1: the runner's approval forwards over the RunChannel. Let the
	// relay to the parent begin.
	ch1 := dialRawRunChannel(t, env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID])
	ch1.sendApproval(t, "appr-1", "item-1")
	require.Eventually(t, func() bool { return relayCount() == 1 }, conformanceWait, 5*time.Millisecond,
		"the approval must relay to the parent as exactly one mail")
	msgID := firstRelayMsg()

	// Reconnect INSIDE the relay window, before the parent replies: kill the
	// stream and redial, then reissue the SAME request_id — exactly what Home
	// does on a real reconnect.
	ch1.close()
	ch2 := dialRawRunChannel(t, env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID])
	defer ch2.close()
	ch2.sendApproval(t, "appr-1", "item-1")

	// The human ACCEPTs, answering the one correlation they were ever shown.
	decision, err := json.Marshal(map[string]any{"decision": "DECISION_ACCEPT", "note": "reviewed"})
	require.NoError(t, err)
	_, err = c.AgentSend(ownerIdentity(), out.Harp, "", "reviewed", decision, msgID)
	require.NoError(t, err)

	// The live channel must carry the ACCEPT — not a DECLINE the human never
	// gave. (Buggy path: the duplicate walk answers here with the bottom-of-
	// ladder DECLINE.)
	got := ch2.awaitApprovalDecision(t, "appr-1", 10*time.Second)
	assert.Equal(t, agentcoordpb.ApprovalDecision_DECISION_ACCEPT, got.GetDecision(),
		"a human ACCEPT must resolve as ACCEPT on the live channel after a reconnect, never silently flip to DECLINE")

	// And there was never a second relay: the reissue reused the in-flight walk
	// rather than minting a duplicate mail/ladder walk. (By the time the live
	// channel has its response, every walk has settled, so the count is final.)
	assert.Equal(t, 1, relayCount(), "a reissue after reconnect must not queue a second approval relay mail")
}
