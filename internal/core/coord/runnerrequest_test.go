package coord

import (
	"context"
	"testing"
	"time"

	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// TestRequestRunner_RoundTrip pins the RunnerChannel's coordinator-initiated
// request/response plumbing (StartRun's transport): a runner dialed in with a
// handler receives a RunnerRequest issued via Coordinator.requestRunner and
// answers it, and the response's request_id round-trips even though
// requestRunner mints it.
//
// The runner is the ONLY one on its credential. A spawned child brings its
// own runner (the fake's Home) on the same credential, and "newest wins"
// then decides — by whichever dialed last — which of the two the request
// reaches; the loser's pending request is answered Unavailable. The plumbing
// under test needs one session, so the credential is minted directly.
func TestRequestRunner_RoundTrip(t *testing.T) {
	resetStrictness(t)
	c := newTestCoordinator(t, newFakeSpawner(t, nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)
	credHash := hashToken(token)

	received := make(chan *agentcoordpb.RunnerRequest, 1)
	handler := func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
		received <- req
		return &agentcoordpb.RunnerResponse{
			Status: &rpcstatus.Status{Message: "started"},
			Kind: &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{
				HarnessSessionId: "native-sess-1",
				Pid:              4242,
			}},
		}
	}
	link, err := runnerHooks.DialRunner(context.Background(), termSink(), c.LoopbackURL(), token, "", "mock", "test", handler)
	require.NoError(t, err)
	t.Cleanup(link.Abort)

	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	// awaitRunner is the registration barrier the spawn path itself uses:
	// the request is issued only once the hello has been taken.
	_, err = c.awaitRunner(ctx, credHash)
	require.NoError(t, err)

	req := RunnerRequest{Kind: StartRun{RunID: "run-under-test"}}
	resp, err := c.requestRunner(ctx, credHash, req)
	require.NoError(t, err)
	require.NotEmpty(t, resp.RequestID, "requestRunner mints a request_id when the caller left it blank")

	select {
	case got := <-received:
		assert.Equal(t, resp.RequestID, got.RequestId, "the runner sees the SAME request_id requestRunner minted")
		assert.Equal(t, "run-under-test", got.GetStartRun().GetRunId())
	case <-time.After(conformanceWait):
		t.Fatal("runner never received the RunnerRequest")
	}

	require.NoError(t, resp.Err, "OK status")
	sr, ok := resp.Kind.(StartRunResult)
	require.True(t, ok)
	assert.Equal(t, "native-sess-1", sr.HarnessSessionID)
	assert.Equal(t, int64(4242), sr.PID)
}

// TestRequestRunner_NoConnectedRunner reports a clear error rather than
// hanging when no runner has registered for the credential yet.
func TestRequestRunner_NoConnectedRunner(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, nil, nil)
	c := newTestCoordinator(t, sp, nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.requestRunner(ctx, "no-such-cred-hash", RunnerRequest{})
	require.Error(t, err)
}

// TestAwaitRunner_WakesOnRegistration pins awaitRunner's ordering: a caller
// that starts waiting BEFORE the runner registers still gets woken, and a
// caller that starts AFTER returns immediately.
//
// Driven on the runner registry directly, under a credential no spawn waits
// on. Dialing a real runner link for a spawned run raced the spawn itself: its
// own awaitRunner woke on the same registration and sent StartRun to a link
// that cannot host it, the launch failed, the session dropped, and the "later"
// caller then waited on the link's redial — past its deadline on a loaded box.
// The waiter's own entry in runnerReady is the latch that it is waiting: only
// it creates one for this credential.
func TestAwaitRunner_WakesOnRegistration(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(t, nil, nil), nil)
	const credHash = "cred-hash-await"

	type woke struct {
		rs  *RunnerSession
		err error
	}
	waited := make(chan woke, 1)
	go func() {
		rs, err := c.awaitRunner(context.Background(), credHash)
		waited <- woke{rs, err}
	}()
	waiting := func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, ok := c.runnerReady[credHash]
		return ok
	}
	require.Eventually(t, waiting, conformanceWait, time.Millisecond, "the waiter never registered")

	rs := c.AttachRunner(Identity{Harp: "child-await", RunID: "run-await"}, credHash, func() {})
	var got woke
	select {
	case got = <-waited:
	case <-time.After(conformanceWait): // a failure report, not a pass condition
		t.Fatal("awaitRunner never woke on registration")
	}
	require.NoError(t, got.err)
	assert.Same(t, rs, got.rs, "the early waiter is woken with the session that registered")

	// A second, later caller finds it already connected — without waiting:
	// its context is already done.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	again, err := c.awaitRunner(ctx, credHash)
	require.NoError(t, err)
	assert.Same(t, rs, again)
}
