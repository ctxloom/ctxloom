package runner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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

// holdAsync runs the permission host for a call and delivers why it answered.
func (h *routeHarness) holdAsync(ctx context.Context, id, tool, input string) <-chan error {
	out := make(chan error, 1)
	args := json.RawMessage(`{"tool":"` + tool + `","input":` + input + `,"tool_use_id":"` + id + `"}`)
	go func() { out <- h.a.hold(ctx, args) }()
	return out
}

func recvErr(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the permission host never answered")
		return nil
	}
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

// TestApprovals_HookBindsTheOldestMatchingOpenSlot: two identical parallel
// calls are held by two host calls; an ask carrying no call id binds the
// OLDEST, so the first call's host is the one the hook's decision supersedes,
// and the second — whose hook never comes — is denied at the arrival grace.
func TestApprovals_HookBindsTheOldestMatchingOpenSlot(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	ctx := context.Background()
	h.toolUse("A", "Bash", lsInput)
	h.toolUse("B", "Bash", lsInput)

	hostA := h.holdAsync(ctx, "A", "Bash", lsInput)
	h.waitArmed(t, 2) // A's anchor wait, then A's slot open and waiting for its hook
	hostB := h.holdAsync(ctx, "B", "Bash", lsInput)
	h.waitArmed(t, 2) // B likewise: A's slot is the older

	hook := make(chan []byte, 1)
	go func() {
		out, err := h.a.Hook(ctx, "PermissionRequest", []byte(`{"tool":"Bash","input":{"command":"ls"}}`))
		assert.NoError(t, err)
		hook <- out
	}()
	<-root.asks
	root.release <- engine.PermissionAnswer{Allow: true}
	assert.True(t, decodeAnswer(t, <-hook).Allow, "the hook carries the root's decision")

	h.waitArmed(t, 2) // the hook's anchor wait, and A's host entering its hold
	select {
	case err := <-hostA:
		t.Fatalf("the host answered (%v) after the hook decided but before the call's result — a host answer may race the hook's", err)
	default:
	}
	h.toolResult("A")
	require.ErrorIs(t, recvErr(t, hostA), errSuperseded, "A was bound: its host answers only after the result, and is superseded")

	h.clock.Advance(approvalArrivalGrace)
	require.ErrorIs(t, recvErr(t, hostB), errHookSilent, "B's hook never came: it is denied at the arrival grace")
}

// TestApprovals_HookWithNoOpenSlotIsDenied: an ask with no call id and no
// host holding the same request open is denied once the anchor wait
// expires — the case of a forged POST from inside an already-permitted
// tool call — and the root is never asked.
func TestApprovals_HookWithNoOpenSlotIsDenied(t *testing.T) {
	asked := false
	h := newRouteHarness(t, func(context.Context, engine.PermissionAsk) (engine.PermissionAnswer, error) {
		asked = true
		return engine.PermissionAnswer{Allow: true}, nil
	})
	h.toolUse("A", "Bash", lsInput) // a call exists, but no host is holding it

	out := make(chan []byte, 1)
	go func() {
		raw, err := h.a.Hook(context.Background(), "PermissionRequest", []byte(`{"tool":"Bash","input":{"command":"ls"}}`))
		assert.NoError(t, err)
		out <- raw
	}()
	h.waitArmed(t, 1)
	h.clock.Advance(approvalAnchorWait)
	ans := decodeAnswer(t, <-out)
	assert.False(t, ans.Allow)
	assert.Equal(t, errNoHostAnchor.Error(), ans.Message)
	assert.False(t, asked, "an unanchored ask never reaches the human")
}

// TestApprovals_RepeatedPostJoinsTheOneDecision: a second POST for a
// request already bound waits on the same decision; the root is asked once.
func TestApprovals_RepeatedPostJoinsTheOneDecision(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	ctx := context.Background()
	h.toolUse("A", "Bash", lsInput)
	hostA := h.holdAsync(ctx, "A", "Bash", lsInput)
	h.waitArmed(t, 2)

	payload := []byte(`{"tool":"Bash","input":{"command":"ls"}}`)
	first, second := make(chan []byte, 1), make(chan []byte, 1)
	go func() { raw, _ := h.a.Hook(ctx, "PermissionRequest", payload); first <- raw }()
	<-root.asks
	go func() { raw, _ := h.a.Hook(ctx, "PermissionRequest", payload); second <- raw }()
	h.waitArmed(t, 2) // both hooks' anchor waits: the second has bound by the time its wait ends
	root.release <- engine.PermissionAnswer{Allow: false, Message: "no"}

	assert.Equal(t, mockAnswer{Allow: false, Message: "no"}, decodeAnswer(t, <-first))
	assert.Equal(t, mockAnswer{Allow: false, Message: "no"}, decodeAnswer(t, <-second))
	select {
	case <-root.asks:
		t.Fatal("the root was asked twice for one request")
	default:
	}
	h.toolResult("A")
	require.ErrorIs(t, recvErr(t, hostA), errSuperseded)
}

// TestApprovals_HostRefusesWhatItCannotAnchor: the host holds only a call
// of this turn whose result has not arrived, matching that call's tool and
// input, and never the host itself.
func TestApprovals_HostRefusesWhatItCannotAnchor(t *testing.T) {
	ctx := context.Background()
	t.Run("a call this turn never made", func(t *testing.T) {
		h := newRouteHarness(t, nil)
		host := h.holdAsync(ctx, "nope", "Bash", lsInput)
		h.waitArmed(t, 1)
		h.clock.Advance(approvalAnchorWait)
		require.ErrorIs(t, recvErr(t, host), errUncorrelated)
	})
	t.Run("another input than the call's", func(t *testing.T) {
		h := newRouteHarness(t, nil)
		h.toolUse("A", "Bash", lsInput)
		require.ErrorIs(t, recvErr(t, h.holdAsync(ctx, "A", "Bash", `{"command":"rm -rf /"}`)), errMismatch)
	})
	t.Run("another tool than the call's", func(t *testing.T) {
		h := newRouteHarness(t, nil)
		h.toolUse("A", "Bash", lsInput)
		require.ErrorIs(t, recvErr(t, h.holdAsync(ctx, "A", "Write", lsInput)), errMismatch)
	})
	t.Run("a call whose result arrived", func(t *testing.T) {
		h := newRouteHarness(t, nil)
		h.toolUse("A", "Bash", lsInput)
		h.toolResult("A")
		require.ErrorIs(t, recvErr(t, h.holdAsync(ctx, "A", "Bash", lsInput)), errUncorrelated)
	})
	t.Run("the host asked about itself", func(t *testing.T) {
		h := newRouteHarness(t, nil)
		h.toolUse("H", "mcp__ctxloom__"+engine.PermissionHostTool, `{}`)
		require.ErrorIs(t, recvErr(t, h.holdAsync(ctx, "H", "mcp__ctxloom__"+engine.PermissionHostTool, `{}`)), errHostCalled)
	})
	t.Run("a model calling the host about its own call", func(t *testing.T) {
		h := newRouteHarness(t, nil)
		h.toolUse("H", "mcp__ctxloom__"+engine.PermissionHostTool, lsInput)
		require.ErrorIs(t, recvErr(t, h.holdAsync(ctx, "H", "Bash", lsInput)), errHostCalled)
	})
}

// TestApprovals_HookLostDeniesTheHeldHost: the bound hook's request ends
// without an answer (the hook was killed), so the held host denies at once
// rather than holding until its bound.
func TestApprovals_HookLostDeniesTheHeldHost(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	h.toolUse("A", "Bash", lsInput)
	hostA := h.holdAsync(context.Background(), "A", "Bash", lsInput)
	h.waitArmed(t, 2)

	hookCtx, killHook := context.WithCancel(context.Background())
	hookDone := make(chan error, 1)
	go func() {
		_, err := h.a.Hook(hookCtx, "PermissionRequest", []byte(`{"tool":"Bash","input":{"command":"ls"}}`))
		hookDone <- err
	}()
	<-root.asks
	killHook()
	require.ErrorIs(t, <-hookDone, context.Canceled)
	require.ErrorIs(t, recvErr(t, hostA), errHookSilent)
}

// TestApprovals_TurnEndReleasesTheHeldHost: the turn's engine process ended
// with the request held; the host is released with a deny.
func TestApprovals_TurnEndReleasesTheHeldHost(t *testing.T) {
	h := newRouteHarness(t, nil)
	h.toolUse("A", "Bash", lsInput)
	hostA := h.holdAsync(context.Background(), "A", "Bash", lsInput)
	h.waitArmed(t, 2)
	h.a.endTurn()
	require.ErrorIs(t, recvErr(t, hostA), errTurnEnded)

	// and the next turn's ledger starts empty
	host := h.holdAsync(context.Background(), "A", "Bash", lsInput)
	h.waitArmed(t, 1)
	h.clock.Advance(approvalAnchorWait)
	require.ErrorIs(t, recvErr(t, host), errUncorrelated)
}

// TestApprovals_ASelfNamedAskIsCorrelatedByItsID: an ask carrying the call's
// id (a pre-tool hook) needs no host: the ledger correlates it, refusing a
// mismatched input, and a host call for the same id later joins its decision.
func TestApprovals_ASelfNamedAskIsCorrelatedByItsID(t *testing.T) {
	root := newBlockingDecide()
	h := newRouteHarness(t, root.decide)
	ctx := context.Background()
	h.toolUse("Q", "AskUserQuestion", `{"questions":[]}`)

	out, err := h.a.Hook(ctx, "PreToolUse", []byte(`{"tool":"AskUserQuestion","input":{"questions":[1]},"tool_use_id":"Q"}`))
	require.NoError(t, err)
	assert.Equal(t, errMismatch.Error(), decodeAnswer(t, out).Message)

	hook := make(chan []byte, 1)
	go func() {
		raw, _ := h.a.Hook(ctx, "PreToolUse", []byte(`{"tool":"AskUserQuestion","input":{"questions":[]},"tool_use_id":"Q"}`))
		hook <- raw
	}()
	ask := <-root.asks
	assert.Equal(t, "Q", ask.ToolUseID)
	root.release <- engine.PermissionAnswer{Allow: true}
	assert.True(t, decodeAnswer(t, <-hook).Allow)

	h.waitArmed(t, 2) // the two hooks' anchor waits, already behind us
	host := h.holdAsync(ctx, "Q", "AskUserQuestion", `{"questions":[]}`)
	h.waitArmed(t, 2) // the host's anchor wait, then its slot open (already bound)
	h.toolResult("Q")
	require.ErrorIs(t, recvErr(t, host), errSuperseded)
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

	stalled := &blockingHome{fakeEngineHome: &fakeEngineHome{}}
	_, err = askTheRoot(stalled, approvalSpec{timeout: time.Minute}, 20*time.Millisecond)(context.Background(), engine.PermissionAsk{Tool: "Bash"})
	require.ErrorIs(t, err, errNoDecision)
	require.True(t, errors.Is(stalled.err, context.DeadlineExceeded), "the bound ended the wait: %v", stalled.err)
}

// blockingHome's Request waits for its context, as a coordinator that never
// answers does.
type blockingHome struct {
	*fakeEngineHome
	err error
}

func (b *blockingHome) Request(ctx context.Context, _ *agentcoordpb.AgentRequest) (*agentcoordpb.CoordinatorResponse, error) {
	<-ctx.Done()
	b.err = ctx.Err()
	return nil, ctx.Err()
}

// TestEngineHost_CapabilitiesGateNamesAMissingApprovalHost: on a human-
// approved run the turn's session start must show ctxloom's server
// connected — it serves the permission host every ask anchors on — or the
// run is warned that this turn's asks will all be denied. An engine that
// reports no server statuses is not judged.
func TestEngineHost_CapabilitiesGateNamesAMissingApprovalHost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		servers []agent.MCPStatus
		warned  bool
	}{
		{"no statuses reported", nil, false},
		{"connected", []agent.MCPStatus{{Name: "ctxloom", Status: "connected"}}, false},
		{"failed", []agent.MCPStatus{{Name: "ctxloom", Status: "failed"}}, true},
		{"absent", []agent.MCPStatus{{Name: "other", Status: "connected"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var findings report.Collector
			eh := NewEngineHost(context.Background(), &findings, "mock", "run-1")
			eh.checkApprovalHost(&agent.ChatSessionInfo{MCPServers: tc.servers})
			warned := false
			for _, text := range findings.All().Texts() {
				warned = warned || strings.Contains(text, "approval host not connected")
			}
			assert.Equal(t, tc.warned, warned, "%v", findings.All().Texts())
		})
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
			bound := home.approvalHost
			home.mu.Unlock()
			assert.Equal(t, tc.bound, bound != nil)
			if tc.bound {
				assert.Same(t, eh.approvals, bound, "the endpoint serves the route the drive feeds")
			}
		})
	}
}
