package testenv

import (
	"context"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawClientProtocolPin is the protocol version the retired hand-rolled
// harness client sent on every handshake: the OLDEST the server supports.
// The tests below hold it only to assert the SDK-driven session negotiates
// something newer, so a harness that quietly slides back to the floor is
// caught.
const rawClientProtocolPin = "2024-11-05"

// startSession spawns `ctxloom mcp` in a fresh isolated environment and
// returns the connected session; both are torn down with the test.
func startSession(t *testing.T) *MCPSession {
	t.Helper()
	env, err := NewTestEnvironment()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Cleanup()) })
	require.NoError(t, env.Setup())
	s, err := env.StartMCP()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, s.Close()) })
	return s
}

func TestMCPSession_Close_NilSessionIsANoOp(t *testing.T) {
	var s *MCPSession
	assert.NoError(t, s.Close())
}

// A returned session has already completed the initialize handshake — there
// is no separate Initialize step whose error could go uninspected.
func TestStartMCP_ReturnsAnInitializedSession(t *testing.T) {
	s := startSession(t)
	res := s.InitializeResult()
	require.NotNil(t, res, "session returned without a captured initialize result")
	assert.NotEmpty(t, res.ProtocolVersion)
	assert.Greater(t, res.ProtocolVersion, rawClientProtocolPin,
		"the SDK client must negotiate newer than the retired client's oldest-version pin")
}

func TestStartMCP_ToolsIteratorFollowsPagesToANonEmptySurface(t *testing.T) {
	s := startSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), MCPCallTimeout)
	defer cancel()
	var names []string
	for tool, err := range s.Tools(ctx, nil) {
		require.NoError(t, err)
		names = append(names, tool.Name)
	}
	assert.NotEmpty(t, names, "ctxloom mcp advertised no tools")
}

// A JSON-RPC error answer is surfaced as a typed jsonrpc.Error, not swallowed
// as success and not misdiagnosed as a timeout: calling a tool the server
// does not have is the cheapest way to make it send one.
func TestStartMCP_JSONRPCErrorSurfacesAsATypedError(t *testing.T) {
	s := startSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), MCPCallTimeout)
	defer cancel()
	_, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "no-such-tool-anywhere"})
	require.Error(t, err)
	var rpcErr *jsonrpc.Error
	require.True(t, errors.As(err, &rpcErr), "want a *jsonrpc.Error, got %T: %v", err, err)
	assert.NotErrorIs(t, err, context.DeadlineExceeded)
}
