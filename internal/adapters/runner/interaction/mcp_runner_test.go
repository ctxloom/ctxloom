package interaction

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Routing-table completeness (B1.6 deliverable 3): every tool either surface
// serves is classified, and the runner surface serves every classified tool
// — an unclassified tool is a registration-time error, never a silent
// fallthrough.

// newTestServer is NewServer over an empty loadout and a discarding
// reporter: registration is what these tests observe, and it reads no
// package bytes.
func newTestServer(harp string, home *runner.Home, leaf bool, cwd string) (*mcp.Server, error) {
	if cwd == "" {
		cwd = "/work"
	}
	return NewServer(report.To(nil), home, harp, cwd, leaf, loadoutSurface{}, NewWakeSignal(nil))
}

// listServerTools connects an in-memory client and lists the server's tools.
func listServerTools(t *testing.T, server *mcp.Server) map[string]*mcp.Tool {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	tools := map[string]*mcp.Tool{}
	cursor := ""
	for {
		page, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{Cursor: cursor})
		require.NoError(t, err)
		for _, tool := range page.Tools {
			tools[tool.Name] = tool
		}
		if page.NextCursor == "" {
			return tools
		}
		cursor = page.NextCursor
	}
}

// testHome builds a Home against a dead loopback endpoint — registration
// needs the value, not a live coordinator.
func testHome(t *testing.T) *runner.Home {
	t.Helper()
	h, err := runner.NewHome(context.Background(), runner.HomeConfig{
		URL:     "http://127.0.0.1:1/mcp",
		Token:   "t",
		RunID:   "run-x",
		Harness: "mock",
		Version: "test",
		Harp:    "run-x-harp",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	return h
}

// TestRunnerServer_ServesExactlyTheClassifiedSurface: the runner's tool set
// IS the routing table — nothing unclassified, nothing missing.
func TestRunnerServer_ServesExactlyTheClassifiedSurface(t *testing.T) {
	server, err := newTestServer("test-harp", testHome(t), false, "")
	require.NoError(t, err)
	tools := listServerTools(t, server)

	routes := mcpschema.Routes()
	for name := range tools {
		_, ok := routes[name]
		assert.True(t, ok, "runner serves unclassified tool %q — classify it in mcpschema.Routes", name)
	}
	for name := range routes {
		_, ok := tools[name]
		assert.True(t, ok, "classified tool %q is not served by the runner surface", name)
	}
}

// TestRunnerServer_CoordinationToolsCarryGeneratedSchemas: the generated
// (proto-canonical) schemas are what the runner advertises.
func TestRunnerServer_CoordinationToolsCarryGeneratedSchemas(t *testing.T) {
	server, err := newTestServer("test-harp", testHome(t), false, "")
	require.NoError(t, err)
	tools := listServerTools(t, server)

	specs, err := mcpschema.Tools()
	require.NoError(t, err)
	for _, spec := range specs {
		tool, ok := tools[spec.Name]
		require.True(t, ok, "generated tool %s missing", spec.Name)
		assert.Equal(t, spec.Description, tool.Description, "%s description is the generated one", spec.Name)
		require.NotNil(t, tool.InputSchema, "%s input schema advertised", spec.Name)
	}
}

// TestRunnerServer_AdvertisesEvaluateTriggers pins the same advertisement on
// the runner surface (where a real harness actually reaches it).
func TestRunnerServer_AdvertisesEvaluateTriggers(t *testing.T) {
	server, err := newTestServer("test-harp", testHome(t), false, "")
	require.NoError(t, err)
	tools := listServerTools(t, server)
	_, ok := tools["evaluate_triggers"]
	assert.True(t, ok, "evaluate_triggers must appear in the runner server's advertised tools")
}
