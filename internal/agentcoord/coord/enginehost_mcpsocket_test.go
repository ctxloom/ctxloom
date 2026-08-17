package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestEngineHost_MCPSocketReadAtStartRunNotConstruction pins WHY startRun
// reads CTXLOOM_MCP_SOCKET where it does. The runner's own MCP endpoint does
// not exist when NewEngineHost is called: standUpRunner builds the host,
// THEN stands the endpoint up and exports its path, and only then calls
// BindHome — the "BIND LAST" ordering that exists because a
// hosted child's shim keys entirely off this variable and stands up a rogue
// local coordinator without it.
//
// So the socket must be resolved at StartRun time, not captured at
// construction: a host built before the export must still deliver the
// exported path into the child's ctxloom MCP entry. This test exports the
// value AFTER construction, in that real order.
func TestEngineHost_MCPSocketReadAtStartRunNotConstruction(t *testing.T) {
	const socket = "/run/user/1000/ctxloom-runner-exported-late.sock"

	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	t.Cleanup(eh.Close)

	// The endpoint is stood up and exported only now — after the host exists.
	t.Setenv(EnvMCPSocket, socket)
	eh.BindHome(home)

	spec, err := buildHarnessSpec(HarnessSpecInput{
		Harness:    "claude-code",
		Model:      "claude-sonnet-5",
		Workspace:  "/work",
		Permission: agent.PermissionBypass,
		MCPServers: []agent.ChatMCPServer{
			{Name: agent.MCPServerName, Command: "ctxloom", Args: []string{"mcp", "forward"}},
			{Name: "unrelated", Command: "other"},
		},
	})
	require.NoError(t, err)
	input, _ := structpb.NewStruct(map[string]any{"prompt": "hello"})
	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{
		StartRun: &agentcoordpb.StartRun{RunId: "run-1", Harness: spec, Input: input, Role: "worker"},
	}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), "StartRun must succeed: %s", resp.GetStatus().GetMessage())

	require.Eventually(t, func() bool {
		sc.mu.Lock()
		defer sc.mu.Unlock()
		return len(sc.requests) == 1
	}, 5*time.Second, 10*time.Millisecond, "the backend must receive its ChatRequest")

	sc.mu.Lock()
	servers := sc.requests[0].MCPServers
	sc.mu.Unlock()
	require.Len(t, servers, 2)

	byName := map[string]agent.ChatMCPServer{}
	for _, s := range servers {
		byName[s.Name] = s
	}
	require.Contains(t, byName, agent.MCPServerName)
	assert.Equal(t, socket, byName[agent.MCPServerName].Env[EnvMCPSocket],
		"the reach-back socket exported AFTER construction must still reach the child's forwarder entry")
	assert.NotContains(t, byName["unrelated"].Env, EnvMCPSocket,
		"only the ctxloom forwarder entry is stamped")
}
