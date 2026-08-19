package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestRequestRunner_RoundTrip pins the RunnerChannel's coordinator-initiated
// request/response plumbing (StartRun's transport): a runner dialed in with a
// handler receives a RunnerRequest issued via Coordinator.requestRunner and
// answers it, and the response's request_id round-trips even though
// requestRunner mints it.
func TestRequestRunner_RoundTrip(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err)
	env := waitForChildEnv(t, c, out.RunID)

	received := make(chan *agentcoordpb.RunnerRequest, 1)
	handler := func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse {
		received <- req
		return &agentcoordpb.RunnerResponse{
			Status: okStatus("started"),
			Kind: &agentcoordpb.RunnerResponse_StartRun{StartRun: &agentcoordpb.StartRunResult{
				HarnessSessionId: "native-sess-1",
				Pid:              4242,
			}},
		}
	}
	link, err := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", handler)
	require.NoError(t, err)
	t.Cleanup(link.cancel)

	credHash := hashToken(env[EnvCoordCred])
	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.runners[credHash] != nil
	}, conformanceWait, 10*time.Millisecond, "the runner must be registered before requestRunner can find it")

	req := &agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: &agentcoordpb.StartRun{
		RunId: out.RunID,
		Harness: &agentcoordpb.HarnessSpec{
			Harness: "mock",
			Model:   "test-model",
		},
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	resp, err := c.requestRunner(ctx, credHash, req)
	require.NoError(t, err)
	require.NotEmpty(t, resp.RequestId, "requestRunner mints a request_id when the caller left it blank")

	select {
	case got := <-received:
		assert.Equal(t, resp.RequestId, got.RequestId, "the runner sees the SAME request_id requestRunner minted")
		assert.Equal(t, out.RunID, got.GetStartRun().GetRunId())
	case <-time.After(conformanceWait):
		t.Fatal("runner never received the RunnerRequest")
	}

	require.Equal(t, int32(0), resp.Status.Code, "OK status")
	sr := resp.GetStartRun()
	require.NotNil(t, sr)
	assert.Equal(t, "native-sess-1", sr.HarnessSessionId)
	assert.Equal(t, int64(4242), sr.Pid)
}

// TestRequestRunner_NoConnectedRunner reports a clear error rather than
// hanging when no runner has registered for the credential yet.
func TestRequestRunner_NoConnectedRunner(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := c.requestRunner(ctx, "no-such-cred-hash", &agentcoordpb.RunnerRequest{})
	require.Error(t, err)
}

// TestAwaitRunner_WakesOnRegistration pins awaitRunner's ordering: a caller
// that starts waiting BEFORE the runner dials in still gets woken, and a
// caller that starts AFTER returns immediately.
func TestAwaitRunner_WakesOnRegistration(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{})
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: agent.RuntimeContainerRootless, profiles: []string{"p1"}}},
		func() *fakeEngine { return &fakeEngine{turnGate: gate} })
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

	link, err := DialRunner(context.Background(), env[EnvCoordURL], env[EnvCoordCred], env[EnvRunID], "mock", "test", nil)
	require.NoError(t, err)
	t.Cleanup(link.cancel)

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
