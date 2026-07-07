package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentbus"
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Hermetic conformance tests for agent delegation (agent_run + the bus),
// mirroring the fan's stub-client pattern: a scriptable fake engine behind
// the ClientFactory seam — no live engines, no containers, HOME scrubbed.

const conformanceWait = 5 * time.Second

// fakeChatEngine is one scripted child engine conversation. Turn texts are
// recorded as received; each turn completes automatically unless gated
// (turnGate non-nil — the test releases turns one by one), and the stream can
// end itself after endAfterTurns turns (an engine that exits).
type fakeChatEngine struct {
	mu            sync.Mutex
	gotReq        agent.ChatRequest
	texts         []string
	turnGate      chan struct{}
	endAfterTurns int
	killed        bool
}

func (f *fakeChatEngine) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	f.mu.Lock()
	f.gotReq = req
	f.mu.Unlock()
	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		defer close(events)
		turns := 0
		for msg := range in {
			if msg.Text == "" {
				continue
			}
			f.mu.Lock()
			f.texts = append(f.texts, msg.Text)
			gate := f.turnGate
			f.mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return
				}
			}
			select {
			case events <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "ok"}}:
			case <-ctx.Done():
				return
			}
			select {
			case events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}:
			case <-ctx.Done():
				return
			}
			turns++
			if f.endAfterTurns > 0 && turns >= f.endAfterTurns {
				return
			}
		}
	}()
	return in, events, errs, nil
}

func (f *fakeChatEngine) recordedTexts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

func (f *fakeChatEngine) request() agent.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotReq
}

func (f *fakeChatEngine) Info(context.Context) (*pb.LLMInfo, error) { return &pb.LLMInfo{}, nil }
func (f *fakeChatEngine) Run(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (int32, error) {
	return 0, nil
}
func (f *fakeChatEngine) RunWithModelInfo(context.Context, *pb.RunStart, io.Reader, io.Writer, io.Writer, <-chan *pb.WindowSize) (*pb.RunResult, error) {
	return &pb.RunResult{}, nil
}
func (f *fakeChatEngine) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, fmt.Errorf("no session")
}
func (f *fakeChatEngine) WatchSession(context.Context, string) (<-chan *pb.WatchEvent, <-chan error, error) {
	return nil, nil, nil
}
func (f *fakeChatEngine) ListSessions(context.Context) ([]agent.SessionMeta, error) { return nil, nil }
func (f *fakeChatEngine) GetPlans(context.Context, string) ([]agent.PlanFile, error) {
	return nil, nil
}
func (f *fakeChatEngine) Kill() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = true
}

// fakeEngineFactory mints one fakeChatEngine per spawn and records the
// backend/label each spawn resolved to (the engine-binding assertion).
type fakeEngineFactory struct {
	mu      sync.Mutex
	next    func() *fakeChatEngine
	engines []*fakeChatEngine
	specs   []string // "backend/label" per spawn
}

func newFakeEngineFactory(next func() *fakeChatEngine) *fakeEngineFactory {
	if next == nil {
		next = func() *fakeChatEngine { return &fakeChatEngine{} }
	}
	return &fakeEngineFactory{next: next}
}

func (ff *fakeEngineFactory) factory(backendName, label string, _ int) (pb.Client, error) {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	e := ff.next()
	ff.engines = append(ff.engines, e)
	ff.specs = append(ff.specs, backendName+"/"+label)
	return e, nil
}

func (ff *fakeEngineFactory) spawnCount() int {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	return len(ff.engines)
}

func (ff *fakeEngineFactory) engine(i int) *fakeChatEngine {
	ff.mu.Lock()
	defer ff.mu.Unlock()
	if i >= len(ff.engines) {
		return nil
	}
	return ff.engines[i]
}

// delegationFixture stages a hermetic project (one bundle fragment, one
// profile composing it, mock-typed engine labels) and the given agents, with
// HOME scrubbed so session accounting and trust stores stay in the sandbox.
func delegationFixture(t *testing.T, subs map[string]agents.Agent) (*config.Config, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(app, "cache", "bundles", "kit1.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f1:\n    content: \"FRAG-ONE\"\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p1.yaml"),
		"bundles:\n  - ctxloom:local@bundles/kit1\n")
	cfg := &config.Config{
		AppPaths: []string{app},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast": {Type: "mock", Body: map[string]any{"model": "m-fast"}},
			},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Agents: subs,
	}
	return cfg, root
}

func writeDelegationFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// newTestOrchestrator builds an orchestrator over the fixture with the fake
// engine factory injected (isolation skipped — fan parity for the seam).
func newTestOrchestrator(t *testing.T, cfg *config.Config, root string, ff *fakeEngineFactory) *agentOrchestrator {
	t.Helper()
	o := newAgentOrchestrator(cfg, "coordinator-harp", 0, root)
	o.factory = ff.factory
	t.Cleanup(func() {
		if o.busSrv != nil {
			_ = o.busSrv.Close()
		}
	})
	return o
}

func headlessAgent(profiles ...string) agents.Agent {
	return agents.Agent{Engine: "fast", Profiles: profiles, Permissions: "bypass"}
}

// TestAgentRun_HonorsAgentIntent pins intent honoring end to end: the
// engine binding picks the backend/label, the composed profile context rides
// the first turn as the lead block ahead of the briefing, the declared
// headless-safe permission enum is applied, the runtime axis is reported, and
// the child's ambient identity (harp/bus/depth) reaches both the engine env
// and the child's own ctxloom MCP entry.
func TestAgentRun_HonorsAgentIntent(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"researcher": headlessAgent("p1"),
	})
	ff := newFakeEngineFactory(nil)
	o := newTestOrchestrator(t, cfg, root, ff)

	out, err := o.run(context.Background(), "researcher", "find the thing")
	require.NoError(t, err)
	assert.NotEmpty(t, out.Harp, "harp minted at enqueue")
	assert.Equal(t, "fast", out.Engine)
	assert.Equal(t, []string{"p1"}, out.Profiles)
	assert.Equal(t, "host", out.Runtime)
	assert.False(t, out.Queued)
	assert.Empty(t, out.Degraded)

	require.Eventually(t, func() bool {
		e := ff.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	assert.Equal(t, []string{"mock/fast"}, ff.specs, "engine binding drives the spawn")
	e := ff.engine(0)
	first := e.recordedTexts()[0]
	assert.Contains(t, first, "FRAG-ONE", "composed profile context leads the first turn")
	assert.Contains(t, first, "find the thing", "the briefing is the first turn")

	req := e.request()
	assert.Equal(t, agent.PermissionBypass, req.Permissions)
	assert.Equal(t, out.Harp, req.Env["CTXLOOM_SESSION_HARP"], "ambient identity, never client-claimed")
	assert.NotEmpty(t, req.Env[agentbus.SocketEnv], "bus reach-back socket")
	assert.Equal(t, "1", req.Env[agentDepthEnv], "depth counter propagates")

	var ctxloomEntry *agent.ChatMCPServer
	for i := range req.MCPServers {
		if req.MCPServers[i].Name == agent.MCPServerName {
			ctxloomEntry = &req.MCPServers[i]
		}
	}
	require.NotNil(t, ctxloomEntry, "the child gets its own ctxloom mcp subprocess")
	assert.Equal(t, out.Harp, ctxloomEntry.Env["CTXLOOM_SESSION_HARP"])
	assert.Equal(t, req.Env[agentbus.SocketEnv], ctxloomEntry.Env[agentbus.SocketEnv])
	assert.Equal(t, "1", ctxloomEntry.Env[agentDepthEnv])
}

// TestAgentRun_RuntimeAxisReported pins that a container-declaring agent's
// resolved runtime rides the enqueue return (the chain itself is prepared at
// launch behind the injected-factory seam — the operations-level gate tests
// cover the refusal).
func TestAgentRun_RuntimeAxisReported(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"builder": {Engine: "fast", Profiles: []string{"p1"}, Runtime: "container", Permissions: "plan"},
	})
	ff := newFakeEngineFactory(nil)
	o := newTestOrchestrator(t, cfg, root, ff)

	out, err := o.run(context.Background(), "builder", "build it")
	require.NoError(t, err)
	assert.Equal(t, "container", out.Runtime)
}

// TestAgentRun_UnknownAgentIsHardError pins run --agent parity: an explicit
// agent name that doesn't resolve is a hard error in every mode.
func TestAgentRun_UnknownAgentIsHardError(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, nil)
	o := newTestOrchestrator(t, cfg, root, newFakeEngineFactory(nil))

	_, err := o.run(context.Background(), "ghost", "boo")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `agent "ghost" not found`)
}

// TestAgentRun_D3RefusesNonHeadless pins the D3 gate: an agent with NO
// permissions field is refused exactly like a non-headless-safe one, with the
// typed strictness finding and the fix-it naming the agent.
func TestAgentRun_D3RefusesNonHeadless(t *testing.T) {
	cases := []struct {
		name   string
		perm   string
		reason string
	}{
		{"absent enum", "", "declares no permissions"},
		{"non-headless enum", "default", "not headless-safe"},
		{"acceptEdits enum", "acceptEdits", "not headless-safe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			cfg, root := delegationFixture(t, map[string]agents.Agent{
				"loose": {Engine: "fast", Profiles: []string{"p1"}, Permissions: tc.perm},
			})
			ff := newFakeEngineFactory(nil)
			o := newTestOrchestrator(t, cfg, root, ff)

			_, err := o.run(context.Background(), "loose", "go")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "fatal finding")
			assert.Contains(t, err.Error(), tc.reason)
			assert.Contains(t, err.Error(), `set permissions: plan|bypass on agent "loose"`)
			assert.Equal(t, 0, ff.spawnCount(), "a refused agent never launches")
		})
	}
}

// TestAgentRun_D3DegradedDowngradesToPlan pins the degraded downgrade: the
// refusal becomes a warning and the child launches at the MOST RESTRICTIVE
// headless-safe enum — degraded narrows a child's permissions, never widens.
func TestAgentRun_D3DegradedDowngradesToPlan(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"loose": {Engine: "fast", Profiles: []string{"p1"}}, // no permissions
	})
	ff := newFakeEngineFactory(nil)
	o := newTestOrchestrator(t, cfg, root, ff)

	out, err := o.run(context.Background(), "loose", "go")
	require.NoError(t, err)
	require.NotEmpty(t, out.Degraded)
	assert.Contains(t, out.Degraded[0], "plan")

	require.Eventually(t, func() bool { return ff.spawnCount() == 1 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool { return len(ff.engine(0).recordedTexts()) == 1 }, conformanceWait, 10*time.Millisecond)
	assert.Equal(t, agent.PermissionPlan, ff.engine(0).request().Permissions)
}

// TestAgentRun_QueuePastCap pins D4/D5: the second spawn past cap=1 ENQUEUES
// (harp minted, queued flag set, no error, no launch) and starts when the
// first child's turn ends — the cap counts executing turns.
func TestAgentRun_QueuePastCap(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	gate := make(chan struct{})
	ff := newFakeEngineFactory(func() *fakeChatEngine { return &fakeChatEngine{turnGate: gate} })
	o := newTestOrchestrator(t, cfg, root, ff)

	first, err := o.run(context.Background(), "worker", "task one")
	require.NoError(t, err)
	assert.False(t, first.Queued)

	// Wait until child 1 is mid-turn (holding the one slot).
	require.Eventually(t, func() bool {
		e := ff.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	second, err := o.run(context.Background(), "worker", "task two")
	require.NoError(t, err)
	assert.True(t, second.Queued, "past the cap the spawn enqueues, never errors")
	assert.NotEmpty(t, second.Harp, "harp minted at enqueue even when queued")
	assert.NotEqual(t, first.Harp, second.Harp)
	assert.Equal(t, 1, ff.spawnCount(), "the queued child must not launch while the slot is held")

	gate <- struct{}{} // finish child 1's turn → idle → slot released
	require.Eventually(t, func() bool { return ff.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		e := ff.engine(1)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	assert.Contains(t, ff.engine(1).recordedTexts()[0], "task two")
}

// TestAgentRun_RecursionGuard pins the depth guard: a process already at
// depth 2 refuses further agent_run with a typed error before any resolution.
func TestAgentRun_RecursionGuard(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	ff := newFakeEngineFactory(nil)
	o := newAgentOrchestrator(cfg, "deep-child-harp", maxAgentDepth, root)
	o.factory = ff.factory

	_, err := o.run(context.Background(), "worker", "go deeper")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegation depth")
	assert.Contains(t, err.Error(), "route further fan-out through its coordinator")
	assert.Equal(t, 0, ff.spawnCount())
}

// TestAgentSend_UnknownRecipient pins the coordinator-side routing error.
func TestAgentSend_UnknownRecipient(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, nil)
	o := newTestOrchestrator(t, cfg, root, newFakeEngineFactory(nil))

	_, err := o.send("nonexistent-harp", "", "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown recipient")

	_, err = o.send(agentbus.ParentAddress, "", "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "has no parent")
}

// TestAgentSend_MidTurnQueuesForBoundary_FIFO pins §6a mid-turn delivery and
// ordering: messages sent while the child executes queue in its mailbox and
// arrive one per turn, per-sender FIFO, at successive boundaries.
func TestAgentSend_MidTurnQueuesForBoundary_FIFO(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	gate := make(chan struct{})
	ff := newFakeEngineFactory(func() *fakeChatEngine { return &fakeChatEngine{turnGate: gate} })
	o := newTestOrchestrator(t, cfg, root, ff)

	_, err := o.run(context.Background(), "worker", "task")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := ff.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	for i := 1; i <= 3; i++ {
		disp, serr := o.send(childHarp(t, o), "", fmt.Sprintf("follow-up %d", i))
		require.NoError(t, serr)
		assert.Contains(t, disp, "queued")
	}

	e := ff.engine(0)
	for turn := 2; turn <= 4; turn++ {
		gate <- struct{}{} // complete the previous turn → boundary drains one message
		want := fmt.Sprintf("follow-up %d", turn-1)
		require.Eventually(t, func() bool {
			texts := e.recordedTexts()
			return len(texts) == turn && texts[turn-1] == want
		}, conformanceWait, 10*time.Millisecond, "turn %d should carry %q", turn, want)
	}
}

// childHarp returns the single child's harp from the orchestrator.
func childHarp(t *testing.T, o *agentOrchestrator) string {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	require.Len(t, o.children, 1)
	for harp := range o.children {
		return harp
	}
	return ""
}

// TestAgentSend_ResumesEndedChild pins §6a resume delivery: a message to a
// child whose engine exited relaunches the session (a fresh spawn primed with
// the agent context; the recorded-history prime degrades gracefully when no
// transcript was bound) and delivers the message as its first turn.
func TestAgentSend_ResumesEndedChild(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	ff := newFakeEngineFactory(func() *fakeChatEngine { return &fakeChatEngine{endAfterTurns: 1} })
	o := newTestOrchestrator(t, cfg, root, ff)

	out, err := o.run(context.Background(), "worker", "task")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		c := o.children[out.Harp]
		return c != nil && c.state == childEnded
	}, conformanceWait, 10*time.Millisecond, "the child should end after its single turn")

	disp, err := o.send(out.Harp, "", "one more thing")
	require.NoError(t, err)
	assert.Contains(t, disp, "resuming")

	require.Eventually(t, func() bool { return ff.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		e := ff.engine(1)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)
	resumedFirst := ff.engine(1).recordedTexts()[0]
	assert.Contains(t, resumedFirst, "one more thing", "the message is the resumed session's first turn")
	assert.Contains(t, resumedFirst, "FRAG-ONE", "the agent's composed context primes the resume")
}

// TestParkedRecvYieldsSlot pins the §6a slot yield end to end: a child parked
// in agent_recv releases its execution slot (the queued child starts), and
// the answer completes the parked recv only after a slot is re-acquired — the
// cap counts EXECUTING turns throughout.
func TestParkedRecvYieldsSlot(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	// Per-engine turn gates: child 1 stays mid-turn (blocked in its tool
	// call) while child 2's turn is released independently.
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var spawned int
	ff := newFakeEngineFactory(nil)
	ff.next = func() *fakeChatEngine {
		e := &fakeChatEngine{turnGate: gates[spawned%len(gates)]}
		spawned++
		return e
	}
	o := newTestOrchestrator(t, cfg, root, ff)

	first, err := o.run(context.Background(), "worker", "ask a question")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := ff.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	second, err := o.run(context.Background(), "worker", "other work")
	require.NoError(t, err)
	require.True(t, second.Queued)
	assert.Equal(t, 1, ff.spawnCount())

	// Child 1, mid-turn, parks in agent_recv (the broker park is the same
	// whether it arrived over the socket or in-process).
	recvDone := make(chan []agentbus.Message, 1)
	go func() {
		msgs, rerr := o.broker.Recv(context.Background(), first.Harp, conformanceWait)
		assert.NoError(t, rerr)
		recvDone <- msgs
	}()

	// The park yields child 1's slot: the queued child 2 starts.
	require.Eventually(t, func() bool { return ff.spawnCount() == 2 }, conformanceWait, 10*time.Millisecond,
		"a parked child must not block the queue")

	// Answering child 1 while child 2 holds the slot: the recv completes only
	// after the slot frees (unpark re-acquires — executing turns stay capped).
	disp, err := o.send(first.Harp, "answer", "42")
	require.NoError(t, err)
	assert.Contains(t, disp, "waiting agent_recv")
	select {
	case <-recvDone:
		t.Fatal("parked recv completed while the slot was still held by child 2")
	case <-time.After(150 * time.Millisecond):
	}

	gates[1] <- struct{}{} // finish child 2's turn → slot frees → child 1 unparks
	select {
	case msgs := <-recvDone:
		require.Len(t, msgs, 1)
		assert.Equal(t, "42", msgs[0].Body)
		assert.Equal(t, "answer", msgs[0].Kind)
	case <-time.After(conformanceWait):
		t.Fatal("parked recv never completed after the slot freed")
	}
}

// TestExecutorReachback_OverSocket pins scope item 3 end to end: a spawned
// child's ctxloom-mcp forwards agent_send/agent_recv over the orchestrator's
// Unix socket under its ambient harp — parent-only routing enforced, the
// report lands in the coordinator's inbox, per-sender FIFO preserved.
func TestExecutorReachback_OverSocket(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	ff := newFakeEngineFactory(nil)
	o := newTestOrchestrator(t, cfg, root, ff)

	out, err := o.run(context.Background(), "worker", "report back")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		e := ff.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	sock := ff.engine(0).request().Env[agentbus.SocketEnv]
	require.NotEmpty(t, sock)

	// The child reports to its parent — and only its parent.
	require.NoError(t, agentbus.ForwardSend(sock, out.Harp, agentbus.ParentAddress, "result", "finding A"))
	require.NoError(t, agentbus.ForwardSend(sock, out.Harp, agentbus.ParentAddress, "result", "finding B"))
	err = agentbus.ForwardSend(sock, out.Harp, "some-sibling-harp", "", "psst")
	require.ErrorIs(t, err, agentbus.ErrPeerRouting)

	msgs, err := o.recv(context.Background(), time.Second)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "finding A", msgs[0].Body)
	assert.Equal(t, "finding B", msgs[1].Body)
	assert.Equal(t, out.Harp, msgs[0].From)
}

// TestAgentRecv_TimeoutIsTypedFailure pins the coordinator-side recv timeout
// (the executor-side mapping is pinned in agentbus's socket tests).
func TestAgentRecv_TimeoutIsTypedFailure(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, nil)
	o := newTestOrchestrator(t, cfg, root, newFakeEngineFactory(nil))

	_, err := o.recv(context.Background(), 20*time.Millisecond)
	require.ErrorIs(t, err, agentbus.ErrRecvTimeout)
}

// TestAgentToolHandlers_PlumbTheDelegation drives the registered tool
// handlers (not the orchestrator directly) for one spawn + send + recv, and
// pins the no-config guard the docgen server relies on.
func TestAgentToolHandlers_PlumbTheDelegation(t *testing.T) {
	resetStrictness(t)
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	ff := newFakeEngineFactory(nil)
	o := newTestOrchestrator(t, cfg, root, ff)
	s := &ctxServer{cfg: cfg, agents: &agentDelegation{selfHarp: "coordinator-harp", orch: o}}

	_, runOut, err := s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "worker", Prompt: "go"})
	require.NoError(t, err)
	require.NotNil(t, runOut)
	assert.NotEmpty(t, runOut.Harp)
	assert.Equal(t, "fast", runOut.Engine)

	require.Eventually(t, func() bool {
		e := ff.engine(0)
		return e != nil && len(e.recordedTexts()) == 1
	}, conformanceWait, 10*time.Millisecond)

	_, sendOut, err := s.handleAgentSend(context.Background(), nil, agentSendInput{To: runOut.Harp, Body: "more"})
	require.NoError(t, err)
	assert.NotEmpty(t, sendOut.Disposition)

	_, _, err = s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
	require.ErrorIs(t, err, agentbus.ErrRecvTimeout)

	bare := &ctxServer{}
	_, _, err = bare.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "x", Prompt: "y"})
	require.ErrorContains(t, err, "agent delegation unavailable")
}
