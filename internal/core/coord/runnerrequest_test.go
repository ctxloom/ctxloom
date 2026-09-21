package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/launch"
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
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)
	credHash := hashToken(token)

	received := make(chan *agentcoordpb.RunnerRequest, 1)
	handler := func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
		received <- req
		return &agentcoordpb.RunnerResponse{
			Status: OKStatus("started"),
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
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.requestRunner(ctx, "no-such-cred-hash", RunnerRequest{})
	require.Error(t, err)
}

// TestAwaitRunner_WakesOnRegistration pins awaitRunner's ordering: a caller
// that starts waiting BEFORE the runner dials in still gets woken, and a
// caller that starts AFTER returns immediately.
func TestAwaitRunner_WakesOnRegistration(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless, profiles: []string{"p1"}}},
		func() *scriptedChat { return &scriptedChat{TurnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	env := waitForChildEnv(t, c, out.RunID)
	credHash := hashToken(env[EnvCoordCred])

	waited := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
		defer cancel()
		_, werr := c.awaitRunner(ctx, credHash)
		waited <- werr
	}()

	// Give the waiter a moment to register before the runner dials in.
	time.Sleep(20 * time.Millisecond)

	link, err := runnerHooks.DialRunner(context.Background(), termSink(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", nil)
	require.NoError(t, err)
	t.Cleanup(link.Abort)

	select {
	case werr := <-waited:
		require.NoError(t, werr)
	case <-time.After(conformanceWait):
		t.Fatal("awaitRunner never woke on registration")
	}

	// A second, later caller finds it already connected.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	rs, err := c.awaitRunner(ctx2, credHash)
	require.NoError(t, err)
	require.NotNil(t, rs)
}
