package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
)

// The delegation CONFORMANCE suite (agent_run intent, queue, D3, recursion,
// send FIFO, resume, parked-recv slot yield, roster, inject, agent_stop) now
// lives against the coordinator's public API in
// internal/agentcoord/coord/conformance_test.go — one state, one place. These
// CLI-level tests pin only the tool-handler plumbing onto that coordinator and
// the no-config guard.

// buildHostCoordinator stands a real (production-spawner) coordinator up over
// a hermetic fixture with HOME scrubbed, serving loopback listeners. It is the
// same standup path `ctxloom run`/`ctxloom acp`/bare `ctxloom mcp` use.
func buildHostCoordinator(t *testing.T, subs map[string]agents.Agent) (*config.Config, *coord.Coordinator) {
	t.Helper()
	resetStrictness(t)
	cfg, root := delegationFixture(t, subs)
	c, err := coord.New(coord.Options{Cfg: cfg, ProjectDir: root, StateDir: t.TempDir(), Factory: fakeChatFactory()})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)
	return cfg, c
}

// TestAgentToolHandlers_PlumbTheDelegation drives the registered tool
// handlers (not the coordinator directly) for one spawn, and pins the
// no-config guard the docgen server relies on.
func TestAgentToolHandlers_PlumbTheDelegation(t *testing.T) {
	cfg, c := buildHostCoordinator(t, map[string]agents.Agent{
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
	assert.Equal(t, "fast", runOut.Engine)

	// agent_send to the spawned child resolves through the handler.
	_, sendOut, err := s.handleAgentSend(context.Background(), nil, agentSendInput{To: runOut.Harp, Body: "more"})
	require.NoError(t, err)
	assert.NotEmpty(t, sendOut.Disposition)

	// agent_recv on the coordinator's own mailbox times out cleanly (the
	// typed contract) when nothing is pending.
	_, _, err = s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
	require.ErrorIs(t, err, coord.ErrRecvTimeout)

	// The no-config guard: a bare server (nil cfg, nil agents) refuses.
	bare := &ctxServer{}
	_, _, err = bare.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "x", Prompt: "y"})
	require.ErrorContains(t, err, "agent delegation unavailable")
}

// TestAgentStopHandler_UnknownChild pins the agent_stop handler's error path.
func TestAgentStopHandler_UnknownChild(t *testing.T) {
	cfg, c := buildHostCoordinator(t, nil)
	s := &ctxServer{
		cfg:    cfg,
		self:   coord.Identity{Harp: "coordinator-harp"},
		agents: &agentDelegation{self: coord.Identity{Harp: "coordinator-harp"}, c: c},
	}
	_, _, err := s.handleAgentStop(context.Background(), nil, agentStopInput{Harp: "ghost"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown session")
}
