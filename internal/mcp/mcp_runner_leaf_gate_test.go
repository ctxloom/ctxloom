package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// Trust-boundary gate (alive-prior / civic-kiwi): a LEAF delegated agent must
// not be handed the coordinator-only MCP tools (agent_run, roster,
// agent_stop, agent_fetch_artifact). Handing a leaf an agent_recv inbox plus
// a roster lets it infer it has children and stall waiting for
// notifications that never arrive — the root cause of the observed bug.
//
// These two tests are the gate's behaviour proof: a leaf runner's advertised
// tool set differs from a coordinator-capable runner's ONLY in withholding
// exactly the four coordinator-only tools; agent_send/agent_recv/agent_report
// (parent reporting) and every cell-local/host-relay/artifact-fetch tool are
// unaffected either way.

// TestRunnerServer_LeafWithholdsCoordinatorOnlyTools proves the gate fires:
// a leaf runner (newRunnerMCPServer(..., leaf=true)) registers agent_send,
// agent_recv, agent_report but NOT agent_run, roster, agent_stop, or
// agent_fetch_artifact.
func TestRunnerServer_LeafWithholdsCoordinatorOnlyTools(t *testing.T) {
	server, err := newRunnerMCPServer(testConfig(), "leaf-harp", testHome(t), true, "")
	require.NoError(t, err)
	tools := listServerTools(t, server)

	for name := range mcpschema.CoordinatorOnlyTools() {
		_, ok := tools[name]
		assert.False(t, ok, "leaf runner must NOT register coordinator-only tool %q", name)
	}

	for _, name := range []string{mcpschema.ToolAgentSend, mcpschema.ToolAgentRecv, mcpschema.ToolAgentReport} {
		_, ok := tools[name]
		assert.True(t, ok, "leaf runner must still register parent-reporting tool %q", name)
	}
}

// TestRunnerServer_CoordinatorCapableRegistersEverything proves the gate is
// session-conditional, not a surface-wide removal: leaf=false (a
// coordinator-capable child, OR the top-level human session where RunID=="")
// registers the complete classified surface, coordinator-only tools
// included — unchanged from before this gate existed.
func TestRunnerServer_CoordinatorCapableRegistersEverything(t *testing.T) {
	server, err := newRunnerMCPServer(testConfig(), "coord-harp", testHome(t), false, "")
	require.NoError(t, err)
	tools := listServerTools(t, server)

	for name := range mcpschema.CoordinatorOnlyTools() {
		_, ok := tools[name]
		assert.True(t, ok, "coordinator-capable runner must register coordinator-only tool %q", name)
	}
}
