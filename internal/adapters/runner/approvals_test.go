package runner

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
)

// routeHarness is one run's approval route on a fake clock, with every
// bounded wait announcing that its timer is armed — the point after which
// advancing the clock expires it — so a test forces each expiry rather than
// waiting one out.
type routeHarness struct {
	a     *approvals
	clock *fakeclock.Clock
	armed chan struct{}
}

func newRouteHarness(t *testing.T, decide func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error)) *routeHarness {
	t.Helper()
	codec, ok := mock.New().Approvals().Get()
	require.True(t, ok)
	h := &routeHarness{clock: fakeclock.New(), armed: make(chan struct{}, 64)}
	h.a = newApprovals(approvalSpec{codec: codec, timeout: time.Minute}, decide)
	h.a.after = h.clock.AfterFunc
	h.a.armed = func() { h.armed <- struct{}{} }
	t.Cleanup(h.a.endTurn) // abandons any decision a test left open
	return h
}

// waitArmed blocks until n more bounded waits have armed their timers.
func (h *routeHarness) waitArmed(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-h.armed:
		case <-time.After(10 * time.Second):
			t.Fatal("a bounded wait never armed")
		}
	}
}

func (h *routeHarness) toolUse(id, tool, input string) {
	h.a.observe(&agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolCallID: id, ToolName: tool, ToolInput: json.RawMessage(input)})
}

func (h *routeHarness) toolResult(id string) {
	h.a.observe(&agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: id})
}

// hookAsync POSTs the mock codec's ask for tool and input — no call id, as
// claude's PermissionRequest carries none — and delivers the answer.
func (h *routeHarness) hookAsync(ctx context.Context, tool, input string) <-chan hookResult {
	out := make(chan hookResult, 1)
	payload := []byte(`{"tool":"` + tool + `","input":` + input + `}`)
	go func() {
		raw, err := h.a.Hook(ctx, wire.HookEventPermissionAsk, payload)
		out <- hookResult{raw: raw, err: err}
	}()
	return out
}

type hookResult struct {
	raw []byte
	err error
}

func recvHook(t *testing.T, ch <-chan hookResult) hookResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("the approval hook never answered")
		return hookResult{}
	}
}

// recvAnswer is the decoded answer of a hook that answered.
func recvAnswer(t *testing.T, ch <-chan hookResult) mockAnswer {
	t.Helper()
	r := recvHook(t, ch)
	require.NoError(t, r.err)
	return decodeAnswer(t, r.raw)
}

// mockAnswer is the mock codec's hook answer.
type mockAnswer struct {
	Allow   bool   `json:"allow"`
	Message string `json:"message"`
}

func decodeAnswer(t *testing.T, raw []byte) mockAnswer {
	t.Helper()
	var a mockAnswer
	require.NoError(t, json.Unmarshal(raw, &a), "%s", raw)
	return a
}

const lsInput = `{"command":"ls"}`

// blockingDecide is a root that records each ask and decides only when told.
type blockingDecide struct {
	asks    chan engine.PermissionAsk
	release chan engine.PermissionAnswer
}

func newBlockingDecide() *blockingDecide {
	return &blockingDecide{asks: make(chan engine.PermissionAsk, 8), release: make(chan engine.PermissionAnswer, 8)}
}

func (b *blockingDecide) decide(ctx context.Context, ask engine.PermissionAsk) (engine.PermissionAnswer, error) {
	b.asks <- ask
	select {
	case ans := <-b.release:
		return ans, nil
	case <-ctx.Done():
		return engine.PermissionAnswer{}, ctx.Err()
	}
}

func (b *blockingDecide) nextAsk(t *testing.T) engine.PermissionAsk {
	t.Helper()
	select {
	case ask := <-b.asks:
		return ask
	case <-time.After(10 * time.Second):
		t.Fatal("the root was never asked")
		return engine.PermissionAsk{}
	}
}

// countingDecide allows at once and counts the asks that reached the root.
type countingDecide struct{ n atomic.Int32 }

func (c *countingDecide) decide(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
	c.n.Add(1)
	return engine.PermissionAnswer{Allow: true}, nil
}

// TestApprovals_AnAskMatchingAnOpenCallReachesTheRoot: the stream announced
// the call, its result has not arrived, and the ask names its exact tool and
// input in another spelling — so the root is asked, about THAT call (the
// ledger stamps its id), and its decision is the hook's answer.
func TestApprovals_AnAskMatchingAnOpenCallReachesTheRoot(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	h.toolUse("A", "Bash", `{"command":"ls","timeout":5}`)

	hook := h.hookAsync(context.Background(), "Bash", `{"timeout":5, "command":"ls"}`)
	ask := root.nextAsk(t)
	assert.Equal(t, "A", ask.ToolUseID, "the ledger names the call the ask matched")
	assert.Equal(t, "Bash", ask.Tool)
	root.release <- engine.PermissionAnswer{Allow: true}
	assert.True(t, recvAnswer(t, hook).Allow)
}

// TestApprovals_AnAskMatchingNoOpenCallIsDeniedUnasked: each way an ask can
// fail to match a call the engine's own stream announced this turn and has
// not closed. The root is never asked: this is what a forged POST gets — a
// request claude never made.
func TestApprovals_AnAskMatchingNoOpenCallIsDeniedUnasked(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *routeHarness)
		tool  string
		input string
	}{
		{"another tool", func(h *routeHarness) { h.toolUse("A", "Bash", lsInput) }, "Read", lsInput},
		{"another input", func(h *routeHarness) { h.toolUse("A", "Bash", lsInput) }, "Bash", `{"command":"rm -rf /"}`},
		{"the call already closed", func(h *routeHarness) { h.toolUse("A", "Bash", lsInput); h.toolResult("A") }, "Bash", lsInput},
		{"a previous turn's call", func(h *routeHarness) { h.toolUse("A", "Bash", lsInput); h.a.endTurn() }, "Bash", lsInput},
		{"no call at all", func(*routeHarness) {}, "Bash", lsInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var root countingDecide
			h := newRouteHarness(t, root.decide)
			tc.setup(h)
			hook := h.hookAsync(context.Background(), tc.tool, tc.input)
			h.waitArmed(t, 1) // the wait for the stream to announce a match
			h.clock.Advance(approvalAnchorWait)
			ans := recvAnswer(t, hook)
			assert.False(t, ans.Allow)
			assert.Equal(t, errUncorrelated.Error(), ans.Message)
			assert.Zero(t, root.n.Load(), "the root is never asked about an uncorrelated request")
		})
	}
}

// TestApprovals_TheHookMayOutrunTheStream: the engine asks before its
// tool_use frame reaches the runner; the ask waits (bounded) for the ledger
// and binds once the call is announced.
func TestApprovals_TheHookMayOutrunTheStream(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	hook := h.hookAsync(context.Background(), "Bash", lsInput)
	h.waitArmed(t, 1)
	h.toolUse("A", "Bash", lsInput)
	assert.Equal(t, "A", root.nextAsk(t).ToolUseID)
	root.release <- engine.PermissionAnswer{Allow: true}
	assert.True(t, recvAnswer(t, hook).Allow)
}

// TestApprovals_IdenticalParallelCallsAreAskedOnceEach: two open calls with
// the same tool and input are two requests; the asks bind them oldest first,
// so each reaches the root once — and a third ask joins a decision rather
// than asking again.
func TestApprovals_IdenticalParallelCallsAreAskedOnceEach(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	h.toolUse("A", "Bash", lsInput)
	h.toolUse("B", "Bash", lsInput)

	first := h.hookAsync(context.Background(), "Bash", lsInput)
	assert.Equal(t, "A", root.nextAsk(t).ToolUseID, "the oldest open call binds first")
	second := h.hookAsync(context.Background(), "Bash", lsInput)
	assert.Equal(t, "B", root.nextAsk(t).ToolUseID, "the next ask binds the next call")
	third := h.hookAsync(context.Background(), "Bash", lsInput)

	root.release <- engine.PermissionAnswer{Allow: true}
	root.release <- engine.PermissionAnswer{Allow: false, Message: "no"}
	got := map[bool]int{}
	for _, ch := range []<-chan hookResult{first, second, third} {
		got[recvAnswer(t, ch).Allow]++
	}
	assert.Len(t, got, 2, "both decisions were handed out")
	select {
	case ask := <-root.asks:
		t.Fatalf("a third ask reached the root: %+v", ask)
	default:
	}
}

// TestApprovals_ARepeatedPostJoinsTheOneDecision: two POSTs for one open call
// park one request; both get its decision.
func TestApprovals_ARepeatedPostJoinsTheOneDecision(t *testing.T) {
	var root countingDecide
	h := newRouteHarness(t, root.decide)
	h.toolUse("A", "Bash", lsInput)
	assert.True(t, recvAnswer(t, h.hookAsync(context.Background(), "Bash", lsInput)).Allow)
	assert.True(t, recvAnswer(t, h.hookAsync(context.Background(), "Bash", lsInput)).Allow)
	assert.Equal(t, int32(1), root.n.Load(), "one request for one call")
}

// TestApprovals_TheFirstPostLeavingDoesNotDecide: the decision is the turn's,
// not the first POST's — a POST that binds and then goes away (a model's own
// curl, killed) cannot turn the genuine hook's wait into a deny.
func TestApprovals_TheFirstPostLeavingDoesNotDecide(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	h.toolUse("A", "Bash", lsInput)

	ctx, cancel := context.WithCancel(context.Background())
	first := h.hookAsync(ctx, "Bash", lsInput)
	root.nextAsk(t)
	cancel()
	require.ErrorIs(t, recvHook(t, first).err, context.Canceled, "nobody reads the answer of a POST that left")

	genuine := h.hookAsync(context.Background(), "Bash", lsInput)
	root.release <- engine.PermissionAnswer{Allow: true}
	assert.True(t, recvAnswer(t, genuine).Allow, "the decision outlives the POST that started it")
}

// TestApprovals_TurnEndDeniesWhatIsStillUndecided: the turn's engine process
// ended with the request open — the root's wait is abandoned, and the hook
// (if anyone still reads it) is denied.
func TestApprovals_TurnEndDeniesWhatIsStillUndecided(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	h.toolUse("A", "Bash", lsInput)
	hook := h.hookAsync(context.Background(), "Bash", lsInput)
	root.nextAsk(t)
	h.a.endTurn()
	ans := recvAnswer(t, hook)
	assert.False(t, ans.Allow)
	assert.Equal(t, errTurnEnded.Error(), ans.Message)
}

// TestApprovals_AnUnreadablePayloadIsAnError: the codec cannot read it, so
// the route decides nothing.
func TestApprovals_AnUnreadablePayloadIsAnError(t *testing.T) {
	var root countingDecide
	h := newRouteHarness(t, root.decide)
	_, err := h.a.Hook(context.Background(), wire.HookEventPermissionAsk, []byte(`not json`))
	require.Error(t, err)
	assert.Zero(t, root.n.Load())
}

// TestApprovals_TheRootsTimeoutDeniesTheAsk: the coordinator's queue denies
// an unanswered request at its approval timeout; that deny is what the hook
// writes back.
func TestApprovals_TheRootsTimeoutDeniesTheAsk(t *testing.T) {
	home := &fakeEngineHome{}
	home.requestFn = func(req *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		require.NotNil(t, req.GetApproval(), "the ask travels as AgentRequest.approval")
		assert.Equal(t, int64(time.Minute.Seconds()), req.GetApproval().GetTimeout().GetSeconds(), "the run's approval timeout rides the request")
		return &agentcoordpb.CoordinatorResponse{
			Status: coordgrpc.OKStatus(""),
			Kind: &agentcoordpb.CoordinatorResponse_Approval{Approval: &agentcoordpb.ApprovalDecision{
				Allow: false, Message: "no decision within 1m0s", Decider: "timeout",
			}},
		}, nil
	}
	ans, err := askTheRoot(home, approvalSpec{timeout: time.Minute}, time.Minute)(context.Background(), engine.PermissionAsk{Tool: "Bash"})
	require.NoError(t, err)
	assert.False(t, ans.Allow)
	assert.Equal(t, "no decision within 1m0s", ans.Message)
}

// TestApprovals_ADeadCoordinatorDeniesAtTheBound: a coordinator that never
// answers is given up on at the request bound, and the ask is denied.
func TestApprovals_ADeadCoordinatorDeniesAtTheBound(t *testing.T) {
	home := &fakeEngineHome{}
	home.requestFn = func(*agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
		return nil, ErrCoordinatorUnreachable
	}
	_, err := askTheRoot(home, approvalSpec{timeout: time.Minute}, time.Minute)(context.Background(), engine.PermissionAsk{Tool: "Bash"})
	require.ErrorIs(t, err, errNoDecision)

	stalled := &blockingHome{fakeEngineHome: &fakeEngineHome{}, done: make(chan error, 1)}
	_, err = askTheRoot(stalled, approvalSpec{timeout: time.Minute}, 20*time.Millisecond)(context.Background(), engine.PermissionAsk{Tool: "Bash"})
	require.ErrorIs(t, err, errNoDecision)
	require.ErrorIs(t, stalled.waitErr(t), context.DeadlineExceeded, "the bound ended the wait")
}

// blockingHome's Request waits for its context, as a coordinator that never
// answers does; entered, when set, is closed once a request is waiting.
type blockingHome struct {
	*fakeEngineHome
	entered chan struct{}
	done    chan error
}

func (b *blockingHome) Request(ctx context.Context, _ *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
	if b.entered != nil {
		close(b.entered)
	}
	<-ctx.Done()
	b.done <- ctx.Err()
	return nil, ctx.Err()
}

// waitErr is why the request's wait ended.
func (b *blockingHome) waitErr(t *testing.T) error {
	t.Helper()
	select {
	case err := <-b.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the root's wait never ended")
		return nil
	}
}

// TestEngineHost_DriveBindsTheApprovalRouteBeforeTheFirstTurn: a run that
// routes approvals has its route bound on the Home — what the endpoint
// serves — before anything is driven; one that does not binds nothing.
func TestEngineHost_DriveBindsTheApprovalRouteBeforeTheFirstTurn(t *testing.T) {
	codec, _ := mock.New().Approvals().Get()
	for _, tc := range []struct {
		name  string
		spec  *approvalSpec
		bound bool
	}{
		{"routes approvals", &approvalSpec{codec: codec, timeout: time.Minute}, true},
		{"does not", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := &fakeEngineHome{}
			eh := NewEngineHost(context.Background(), nil, string(mock.Name), "run-1")
			t.Cleanup(eh.Close)
			eh.BindHome(home)
			inst, err := mock.New().Instance(engine.Session{Mode: engine.Structured, WorkDir: t.TempDir()})
			require.NoError(t, err)
			require.NoError(t, eh.Drive(context.Background(), Turn{
				Launch:   launch.Launch{Engine: mock.Name, Mode: engine.Structured},
				Instance: inst, approval: tc.spec,
			}))
			home.mu.Lock()
			bound := home.approvalRoute
			home.mu.Unlock()
			assert.Equal(t, tc.bound, bound != nil)
			if tc.bound {
				assert.Same(t, eh.approvals, bound, "the endpoint serves the route the drive feeds")
			}
		})
	}
}
