package grpc

import (
	"testing"

	"github.com/ctxloom/shared/agent"
	"github.com/ctxloom/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The host ships AssembleManagedConfig's payload to the plugin over RunStart, so
// every field the launch path depends on must survive the proto round-trip.
// BundleMCP carries the profile/builtin bundle servers (taskloom,
// sequential-thinking, …); if it's dropped here, the plugin-side Flush writes
// .mcp.json without them and they never register — the wire-boundary twin of the
// Flush(nil) leak.
func TestManagedConfig_ProtoRoundTrip_PreservesBundleMCP(t *testing.T) {
	in := &agent.ManagedConfig{
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{}},
		BundleMCP: map[string]wire.MCPServer{
			"taskloom": {Command: "taskloom", Args: []string{"mcp"}, SCM: "bundle:builtin:taskloom"},
		},
		ManageStatusline: true,
	}

	got := managedConfigFromProto(ManagedConfigToProto(in))

	require.NotNil(t, got)
	require.NotNil(t, got.BundleMCP, "BundleMCP dropped across the proto round-trip")
	require.Contains(t, got.BundleMCP, "taskloom")
	assert.Equal(t, "taskloom", got.BundleMCP["taskloom"].Command)
	assert.Equal(t, []string{"mcp"}, got.BundleMCP["taskloom"].Args)
	assert.Equal(t, "bundle:builtin:taskloom", got.BundleMCP["taskloom"].SCM)
}
