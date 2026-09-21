package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/coord/coordtest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The delegation CONFORMANCE suite (agent_run intent, queue, D3, recursion,
// send FIFO, resume, parked-recv slot yield, roster, inject, agent_stop) now
// lives against the coordinator's public API in
// internal/core/coord/conformance_test.go — one state, one place. These
// CLI-level tests pin only the tool-handler plumbing onto that coordinator and
// the no-config guard.

// buildHostCoordinator stands a real (production-spawner) coordinator up over
// a hermetic fixture with HOME scrubbed, serving loopback listeners. It is the
// same standup path `ctxloom run`/bare `ctxloom mcp` use. The
// returned *coordtest.Runners lets a test reach into a spawned child's
// captured ChatRequest (see coordtest.Engine.Request) instead of only
// observing that a harp came back.
func buildHostCoordinator(t *testing.T, subs map[string]agents.Agent) (*config.Config, *coord.Coordinator, *coordtest.Runners) {
	t.Helper()
	resetStrictness(t)
	cfg, root := delegationFixture(t, subs)
	runners := coordtest.NewRunners()
	t.Cleanup(runners.Close)
	c, err := coord.New(coord.Options{Spawner: spawn.New(nil, fixtureApp(t, cfg), root, runners.Starter), ProjectDir: root, StateDir: t.TempDir(), OwnerHarp: "coordinator-harp"})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	return cfg, c, runners
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
	engine := spawns.AwaitEngine(t, 0)
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
	_, sendOut, err := s.handleAgentSend(context.Background(), nil, agentSendInput{To: runOut.Harp, Body: "more", Kind: coord.KindMessage})
	require.NoError(t, err)
	assert.NotEmpty(t, sendOut.Disposition)

	// The child's turn output reaches the coordinator's inbox AUTOMATICALLY:
	// its runner files the turn report into the child's own out/ spool and
	// the coordinator routes it. The real mock backend echoes each turn as
	// "mock chat: <text>", so the report carries the child's own output.
	var recvOut *agentRecvResult
	var gotResult bool
	require.Eventually(t, func() bool {
		_, recvOut, err = s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
		if err != nil || recvOut == nil {
			return false
		}
		for _, m := range recvOut.Messages {
			if m.Kind == "result" && strings.HasPrefix(m.Body, "mock chat:") {
				gotResult = true
			}
		}
		return gotResult
	}, 10*time.Second, 10*time.Millisecond, "the child's result must reach the coordinator's inbox without the child choosing to report")

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
// servers — any discovered companion loadout's, ctxloom's own included — so
// the assertion is containment, not exact-set equality.) Each bundle also
// carries a fragment: a profile that assembles to no context at all is
// refused at launch, and this fixture reads no companion loadout to supply
// one.
func TestProdSpawner_ChildMCPServers_ScopedPerAgent(t *testing.T) {
	cfg, root := delegationFixture(t, map[string]agents.Agent{
		"workerA": headlessAgent("p-a"),
		"workerB": headlessAgent("p-b"),
	})
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(paths.LocalBundlesPathFor(app, paths.LayoutV2), "kit-a.yaml"),
		"version: \"1.0.0\"\nfragments:\n  a:\n    content: KIT-A\nmcp:\n  server-a:\n    command: echo\n    args: [\"a\"]\n")
	writeDelegationFile(t, filepath.Join(paths.LocalBundlesPathFor(app, paths.LayoutV2), "kit-b.yaml"),
		"version: \"1.0.0\"\nfragments:\n  b:\n    content: KIT-B\nmcp:\n  server-b:\n    command: echo\n    args: [\"b\"]\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-a.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-a\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-b.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-b\n")

	resetStrictness(t)
	spawns := coordtest.NewRunners()
	t.Cleanup(spawns.Close)
	c, err := coord.New(coord.Options{Spawner: spawn.New(nil, fixtureApp(t, cfg), root, spawns.Starter), ProjectDir: root, StateDir: t.TempDir(), OwnerHarp: "coordinator-harp"})
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
	engine0 := spawns.AwaitEngine(t, 0)
	engine1 := spawns.AwaitEngine(t, 1)
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
	writeDelegationFile(t, filepath.Join(paths.LocalBundlesPathFor(app, paths.LayoutV2), "kit-a.yaml"),
		"version: \"1.0.0\"\nfragments:\n  a:\n    content: KIT-A\nmcp:\n  server-a:\n    command: echo\n    args: [\"a\"]\n")
	writeDelegationFile(t, filepath.Join(paths.LocalBundlesPathFor(app, paths.LayoutV2), "kit-b.yaml"),
		"version: \"1.0.0\"\nfragments:\n  b:\n    content: KIT-B\nmcp:\n  server-b:\n    command: echo\n    args: [\"b\"]\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-a.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-a\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p-b.yaml"), "bundles:\n  - ctxloom:local@bundles/kit-b\n")

	resetStrictness(t)
	spawns := coordtest.NewRunners()
	t.Cleanup(spawns.Close)
	c, err := coord.New(coord.Options{Spawner: spawn.New(nil, fixtureApp(t, cfg), root, spawns.Starter), ProjectDir: root, StateDir: t.TempDir(), OwnerHarp: "coordinator-harp"})
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
		if len(result.Runs) != 2 {
			return false
		}
		runs = nil
		for _, r := range result.Runs {
			role := ""
			if r.Agent != nil {
				role = r.Agent.Role
			}
			runs = append(runs, &agentRunInfoWant{role: role, perm: r.PermissionMode, servers: r.MCPServers})
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

// TestAgentStopHandler_OmittedHarpIsTheBulkForm pins the stdio server's
// agent_stop with NO harp: every live child of this session is stopped and
// each is named in the result; omitting the reason as well is refused,
// naming what is missing, and stops nothing.
func TestAgentStopHandler_OmittedHarpIsTheBulkForm(t *testing.T) {
	cfg, c, spawns := buildHostCoordinator(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	s := &ctxServer{
		cfg:    cfg,
		self:   coord.Identity{Harp: "coordinator-harp", Depth: 0},
		agents: &agentDelegation{self: coord.Identity{Harp: "coordinator-harp", Depth: 0}, c: c},
	}
	_, runOut, err := s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "worker", Prompt: "go"})
	require.NoError(t, err)
	spawns.AwaitEngine(t, 0)
	// Sweep an IDLE child: a turn still in flight is given the real drain
	// bound (agent_recv's max wait), which this package cannot shrink — the
	// bound itself is pinned in coord's own tests.
	require.Eventually(t, func() bool {
		for _, e := range c.Roster(c.Owner()) {
			if e.Harp == runOut.Harp && e.State == coord.StateIdle {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond, "the child never reached its turn boundary")

	_, _, err = s.handleAgentStop(context.Background(), nil, agentStopInput{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reason")
	assert.Contains(t, err.Error(), "harp")

	_, stopOut, err := s.handleAgentStop(context.Background(), nil, agentStopInput{Reason: "batch done"})
	require.NoError(t, err)
	require.NotNil(t, stopOut)
	require.Len(t, stopOut.Children, 1, "the result names each child: %+v", stopOut)
	assert.Equal(t, runOut.Harp, stopOut.Children[0].Harp)
	assert.NotEmpty(t, stopOut.Children[0].Outcome)
	assert.Contains(t, stopOut.Children[0].Detail, "batch done")
	for _, e := range c.Roster(c.Owner()) {
		assert.Equal(t, coord.StateEnded, e.State, "roster afterwards shows none live: %+v", e)
	}
}
