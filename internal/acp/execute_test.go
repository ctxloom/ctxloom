package acp

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// executeHarness wires a caller-built ACP backend to a fakeAgent over
// in-memory pipes — the Execute-path counterpart of startChat.
func executeHarness(t *testing.T, b *ACP) *fakeAgent {
	t.Helper()
	c2aR, c2aW := io.Pipe() // client → agent
	a2cR, a2cW := io.Pipe() // agent → client

	b.openTransport = func(context.Context, transportRequest) (*transport, error) {
		return &transport{
			stdin:  c2aW,
			stdout: a2cR,
			close: func() error {
				_ = c2aW.Close()
				_ = a2cR.Close()
				return nil
			},
		}, nil
	}
	return newFakeAgent(c2aR, a2cW)
}

// captureSessionNew scripts a full handshake + one turn on the fake agent and
// returns the channel carrying the session/new params.
func captureSessionNew(fa *fakeAgent) <-chan json.RawMessage {
	newParams := make(chan json.RawMessage, 1)
	go func() {
		initReq := <-fa.requests
		_ = fa.respond(initReq.ID, map[string]any{"protocolVersion": 1})
		newReq := <-fa.requests
		newParams <- newReq.Params
		_ = fa.respond(newReq.ID, map[string]any{"sessionId": "s"})
		promptReq := <-fa.requests
		_ = fa.respond(promptReq.ID, map[string]any{"stopReason": "end_turn"})
	}()
	return newParams
}

// sessionNewMCP decodes the mcpServers slice of a session/new request.
type sessionNewMCP struct {
	McpServers []struct {
		Name    string   `json:"name"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"mcpServers"`
}

// TestExecute_InjectsManagedMCPServersAtSessionNew: the Setup→Execute sequence
// (the Run RPC's oneshot path) delivers the managed MCP set — ctxloom's
// auto-registered context server plus the builtin taskloom bundle server —
// through session/new mcpServers: the structured path reads no engine settings
// file, so the injection is its counterpart of Setup's settings write.
func TestExecute_InjectsManagedMCPServersAtSessionNew(t *testing.T) {
	b := NewACP()
	fa := executeHarness(t, b)

	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir: t.TempDir(),
		Managed: &agent.ManagedConfig{
			MCP:       &wire.MCPConfig{},
			BundleMCP: map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}},
		},
	}))
	newParams := captureSessionNew(fa)

	_, err := b.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "hi"},
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	var got sessionNewMCP
	require.NoError(t, json.Unmarshal(<-newParams, &got))
	require.Len(t, got.McpServers, 2)
	assert.Equal(t, "ctxloom", got.McpServers[0].Name)
	// Command names the self-exec absolute path (agent.CtxloomCommand), not
	// the bare name — see its doc for the staged-vs-installed invariant.
	assert.Equal(t, agent.CtxloomCommand(), got.McpServers[0].Command)
	// `mcp serve` is ctxloom's machine surface; bare `mcp` lists the
	// configured servers for a human, so an engine handed it would get a
	// listing where it expected a protocol.
	assert.Equal(t, []string{"mcp", "serve"}, got.McpServers[0].Args)
	assert.Equal(t, "taskloom", got.McpServers[1].Name)
	assert.Equal(t, []string{"mcp"}, got.McpServers[1].Args)
}

// TestExecute_NoSetupInjectsNothing: without a Setup-merged managed payload
// (the skip-setup fan-out), Execute injects no servers — mirroring the
// settings write that never happened.
func TestExecute_NoSetupInjectsNothing(t *testing.T) {
	b := NewACP()
	fa := executeHarness(t, b)
	newParams := captureSessionNew(fa)

	_, err := b.Execute(context.Background(), &agent.ExecuteRequest{
		Mode:   agent.ModeOneshot,
		Prompt: &agent.Fragment{Content: "hi"},
	}, io.Discard, io.Discard)
	require.NoError(t, err)

	var got sessionNewMCP
	require.NoError(t, json.Unmarshal(<-newParams, &got))
	assert.Empty(t, got.McpServers)
}
