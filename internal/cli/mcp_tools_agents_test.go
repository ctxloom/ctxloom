package cli

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The delegation CONFORMANCE suite (agent_run intent, queue, D3, recursion,
// send FIFO, resume, parked-recv slot yield, roster, inject, agent_stop) now
// lives against the coordinator's public API in
// internal/agentcoord/coord/conformance_test.go — one state, one place. These
// CLI-level tests pin only the tool-handler plumbing onto that coordinator and
// the no-config guard.

// buildHostCoordinator stands a real (production-spawner) coordinator up over
// a hermetic fixture with HOME scrubbed, serving loopback listeners. It is the
// same standup path `ctxloom run`/`ctxloom acp`/bare `ctxloom mcp` use. The
// returned *fakeChatEngineSpawns lets a test reach into a spawned child's
// captured ChatRequest (see fakeChatEngineSpawns.nth) instead of only
// observing that a harp came back.
func buildHostCoordinator(t *testing.T, subs map[string]agents.Agent) (*config.Config, *coord.Coordinator, *fakeChatEngineSpawns) {
	t.Helper()
	resetStrictness(t)
	cfg, root := delegationFixture(t, subs)
	spawns := &fakeChatEngineSpawns{}
	c, err := coord.New(coord.Options{Cfg: cfg, ProjectDir: root, StateDir: t.TempDir(), Factory: spawns.factory()})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	return cfg, c, spawns
}

// TestAgentToolHandlers_PlumbTheDelegation drives the registered tool
// handlers (not the coordinator directly) for one spawn, and pins the
// no-config guard the docgen server relies on. It also pins what the spawn
// ACTUALLY carried to the child: before this test captured the ChatRequest,
// a completely wrong permission or an empty context would still have shown
// a non-empty harp and Engine=="fast" — the plumbing looked fine while
// silently losing the two things a delegated child most needs correct.
func TestAgentToolHandlers_PlumbTheDelegation(t *testing.T) {
	cfg, c, spawns := buildHostCoordinator(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	s := &ctxServer{
		cfg:  cfg,
		self: coord.Identity{Harp: "coordinator-harp", Depth: 0},
		agents: &agentDelegation{
			self: coord.Identity{Harp: "coordinator-harp", Depth: 0},
			c:    c,
		},
	}

	_, runOut, err := s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "worker", Prompt: "go"})
	require.NoError(t, err)
	require.NotNil(t, runOut)
	assert.NotEmpty(t, runOut.Harp)
	assert.Equal(t, "fast", runOut.LLM)

	// The spawned child's engine actually received the composed agent context
	// (the "FRAG-ONE" fragment content delegationFixture seeds into bundle
	// kit1/profile p1) as its first turn, and the agent's declared permission
	// ("bypass" — see headlessAgent), not some default or leftover value.
	engine := waitForChatCapture(t, spawns, 0)
	capturedReq, _ := engine.Request()
	assert.Equal(t, agent.PermissionBypass, capturedReq.Permissions,
		"the child must launch with the agent's declared permission, not a default")

	var texts []string
	require.Eventually(t, func() bool {
		texts = engine.Texts()
		return len(texts) > 0
	}, 5*time.Second, 5*time.Millisecond, "the child never received its lead-context turn")
	assert.Contains(t, texts[0], "FRAG-ONE",
		"the child's first turn must carry the real composed fragment content, not an empty/placeholder context")

	// agent_send to the spawned child resolves through the handler.
	_, sendOut, err := s.handleAgentSend(context.Background(), nil, agentSendInput{To: runOut.Harp, Body: "more"})
	require.NoError(t, err)
	assert.NotEmpty(t, sendOut.Disposition)

	// The child's turn output reaches the coordinator's mailbox
	// AUTOMATICALLY now — the blunt-whiff bridge (coord/children.go
	// bridgeTurnResult), which fires for a legacy chat child too, no longer
	// depending on the backend NOT implementing StructuredChat. The fake
	// chat engine's assistant output for each turn is "ok".
	var recvOut *agentRecvResult
	require.Eventually(t, func() bool {
		_, recvOut, err = s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
		return err == nil && recvOut != nil && len(recvOut.Messages) > 0
	}, 5*time.Second, 10*time.Millisecond, "the child's result must reach the coordinator's mailbox without the child choosing to report")
	var gotResult bool
	for _, m := range recvOut.Messages {
		if m.Kind == "result" && m.Body == "ok" {
			gotResult = true
		}
	}
	assert.True(t, gotResult, "the bridged turn result must carry the child's own output; got %+v", recvOut.Messages)

	// The no-config guard: a bare server (nil cfg, nil agents) refuses.
	bare := &ctxServer{}
	_, _, err = bare.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "x", Prompt: "y"})
	require.ErrorContains(t, err, "agent delegation unavailable")
}

// TestAgentStopHandler_UnknownChild pins the agent_stop handler's error path.
func TestAgentStopHandler_UnknownChild(t *testing.T) {
	cfg, c, _ := buildHostCoordinator(t, nil)
	s := &ctxServer{
		cfg:    cfg,
		self:   coord.Identity{Harp: "coordinator-harp"},
		agents: &agentDelegation{self: coord.Identity{Harp: "coordinator-harp"}, c: c},
	}
	_, _, err := s.handleAgentStop(context.Background(), nil, agentStopInput{Harp: "ghost"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown session")
}

// TestProdSpawner_ChildMCPServers_ScopedPerAgent is the privilege-scoping
// guarantee prodSpawner.childMCPServers (spawner.go) exists for, and which had
// zero test references anywhere before this: each agent's spawned child must
// carry ONLY the MCP servers its OWN bundle-declared profiles bring in, never
// a sibling agent's (and transitively never a channel to reach them). Two
// agents here declare disjoint profiles, each pulling in one bundle with one
// distinctly-named MCP server; each captured child request must show its own
// server and must NOT show the other's. (Both may also carry shared baseline
// servers — the builtin ctxloom server, any discovered companion loadout —
// so the assertion is containment, not exact-set equality.)
func TestProdSpawner_ChildMCPServers_ScopedPerAgent(t *testing.T) {
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"workerA": headlessAgent("p-a"),
		"workerB": headlessAgent("p-b"),
	})
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(app, "content", "bundles", "kit-a.yaml"),
		"version: \"1.0.0\"\nmcp:\n  server-a:\n    command: echo\n    args: [\"a\"]\n")
	writeDelegationFile(t, filepath.Join(app, "content", "bundles", "kit-b.yaml"),
		"version: \"1.0.0\"\nmcp:\n  server-b:\n    command: echo\n    args: [\"b\"]\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-a.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-a\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-b.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-b\n")

	resetStrictness(t)
	spawns := &fakeChatEngineSpawns{}
	c, err := coord.New(coord.Options{Cfg: cfg, ProjectDir: root, StateDir: t.TempDir(), Factory: spawns.factory()})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	s := &ctxServer{
		cfg:  cfg,
		self: coord.Identity{Harp: "coordinator-harp", Depth: 0},
		agents: &agentDelegation{
			self: coord.Identity{Harp: "coordinator-harp", Depth: 0},
			c:    c,
		},
	}

	_, _, err = s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "workerA", Prompt: "go"})
	require.NoError(t, err)
	_, _, err = s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "workerB", Prompt: "go"})
	require.NoError(t, err)

	// AgentRun enqueues and spawns on a separate goroutine (children.go's
	// `go c.runChild(...)`), so which of the two children reaches the
	// factory FIRST is not guaranteed to match call order. Rather than
	// assume spawn #0 is workerA, check the property that actually matters
	// across BOTH captured requests: exactly one child carries server-a (and
	// not server-b), exactly one carries server-b (and not server-a), and no
	// single child ever carries both.
	engine0 := waitForChatCapture(t, spawns, 0)
	engine1 := waitForChatCapture(t, spawns, 1)
	req0, _ := engine0.Request()
	req1, _ := engine1.Request()

	countWith := func(name string) int {
		n := 0
		for _, req := range []agent.ChatRequest{req0, req1} {
			if hasMCPServer(req.MCPServers, name) {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, countWith("server-a"), "exactly one child must carry server-a")
	assert.Equal(t, 1, countWith("server-b"), "exactly one child must carry server-b")
	for i, req := range []agent.ChatRequest{req0, req1} {
		assert.False(t, hasMCPServer(req.MCPServers, "server-a") && hasMCPServer(req.MCPServers, "server-b"),
			"child #%d must not carry BOTH agents' MCP servers", i)
	}
}

// TestProdSpawner_ChildMCPServers_JournaledDisjointPerAgent is the JOURNAL
// side of TestProdSpawner_ChildMCPServers_ScopedPerAgent's guarantee: the
// same two disjoint-profile agents, but instead of inspecting what the
// engine's ChatRequest received, this asserts what the "roster" MCP tool
// (backed by Coordinator.ListRuns, see consumer.go's listRunsSnapshot)
// reports — the only signal a REAL operator auditing their own coordinator
// actually has. Before this test (and the fields it pins), roster showed
// nothing about a child's permission or MCP servers at all.
func TestProdSpawner_ChildMCPServers_JournaledDisjointPerAgent(t *testing.T) {
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"workerA": headlessAgent("p-a"),
		"workerB": headlessAgent("p-b"),
	})
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(app, "content", "bundles", "kit-a.yaml"),
		"version: \"1.0.0\"\nmcp:\n  server-a:\n    command: echo\n    args: [\"a\"]\n")
	writeDelegationFile(t, filepath.Join(app, "content", "bundles", "kit-b.yaml"),
		"version: \"1.0.0\"\nmcp:\n  server-b:\n    command: echo\n    args: [\"b\"]\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-a.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-a\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-b.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-b\n")

	resetStrictness(t)
	spawns := &fakeChatEngineSpawns{}
	c, err := coord.New(coord.Options{Cfg: cfg, ProjectDir: root, StateDir: t.TempDir(), Factory: spawns.factory()})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	s := &ctxServer{
		cfg:  cfg,
		self: coord.Identity{Harp: "coordinator-harp", Depth: 0},
		agents: &agentDelegation{
			self: coord.Identity{Harp: "coordinator-harp", Depth: 0},
			c:    c,
		},
	}

	_, _, err = s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "workerA", Prompt: "go"})
	require.NoError(t, err)
	_, _, err = s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "workerB", Prompt: "go"})
	require.NoError(t, err)

	var runs []*agentRunInfoWant
	require.Eventually(t, func() bool {
		result := c.ListRuns(true, "")
		if len(result.GetRuns()) != 2 {
			return false
		}
		runs = nil
		for _, r := range result.GetRuns() {
			runs = append(runs, &agentRunInfoWant{role: r.GetAgent().GetRole(), perm: r.GetPermissionMode(), servers: r.GetMcpServers()})
		}
		return true
	}, 5*time.Second, 5*time.Millisecond, "roster never surfaced both spawned children")

	var infoA, infoB *agentRunInfoWant
	for _, r := range runs {
		switch r.role {
		case "workerA":
			infoA = r
		case "workerB":
			infoB = r
		}
	}
	require.NotNil(t, infoA, "roster must surface workerA's run")
	require.NotNil(t, infoB, "roster must surface workerB's run")

	assert.Equal(t, "bypass", infoA.perm, "roster must surface the child's resolved permission mode")
	assert.Equal(t, "bypass", infoB.perm)
	assert.Contains(t, infoA.servers, "server-a")
	assert.NotContains(t, infoA.servers, "server-b", "workerA's roster entry must never list its sibling's MCP server")
	assert.Contains(t, infoB.servers, "server-b")
	assert.NotContains(t, infoB.servers, "server-a", "workerB's roster entry must never list its sibling's MCP server")
}

// agentRunInfoWant is a minimal projection of ListRunsResult_RunInfo for the
// journaled-disjoint-per-agent assertion above.
type agentRunInfoWant struct {
	role    string
	perm    string
	servers []string
}

func hasMCPServer(servers []agent.ChatMCPServer, name string) bool {
	for _, s := range servers {
		if s.Name == name {
			return true
		}
	}
	return false
}

// waitForChatCapture polls until the nth spawned engine exists and has
// captured a Chat call. AgentRun enqueues and returns before the spawn
// actually runs (`go c.runChild(...)` in children.go) — the harp/Engine
// come back synchronously, but the factory call (and so the captured
// ChatRequest) lands on a separate goroutine shortly after.
func waitForChatCapture(t *testing.T, spawns *fakeChatEngineSpawns, n int) *fakeChatEngine {
	t.Helper()
	var engine *fakeChatEngine
	require.Eventually(t, func() bool {
		engine = spawns.nth(n)
		if engine == nil {
			return false
		}
		_, ok := engine.Request()
		return ok
	}, 5*time.Second, 5*time.Millisecond, "spawn #%d never reached the engine's Chat call", n)
	return engine
}
