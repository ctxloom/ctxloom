package cli

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
	"github.com/ctxloom/ctxloom/internal/config"
)

// Routing-table completeness (B1.6 deliverable 3): every tool either surface
// serves is classified, and the runner surface serves every classified tool
// — an unclassified tool is a registration-time error, never a silent
// fallthrough.

func testConfig() *config.Config {
	return &config.Config{
		LM:       config.LMConfig{Configs: map[string]config.LLMConfig{}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
	}
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
func testHome(t *testing.T) *coord.Home {
	t.Helper()
	h, err := coord.NewHome(context.Background(), coord.HomeConfig{
		URL:     "http://127.0.0.1:1/mcp",
		Token:   "t",
		RunID:   "run-x",
		Harness: "mock",
		Version: "test",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	return h
}

// TestRunnerServer_ServesExactlyTheClassifiedSurface: the runner's tool set
// IS the routing table — nothing unclassified, nothing missing.
func TestRunnerServer_ServesExactlyTheClassifiedSurface(t *testing.T) {
	server, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t))
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
	server, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t))
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

// TestRunnerServer_HostRelayDescriptionsMatchStdio pins the description
// parity between the runner's host-relay registrations and the stdio
// server's typed ones — the two surfaces must not drift.
func TestRunnerServer_HostRelayDescriptionsMatchStdio(t *testing.T) {
	runner, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t))
	require.NoError(t, err)
	runnerTools := listServerTools(t, runner)

	s := &ctxServer{cfg: testConfig()}
	stdio := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(stdio)
	stdioTools := listServerTools(t, stdio)

	for name, route := range mcpschema.Routes() {
		if route != mcpschema.RouteHostRelay {
			continue
		}
		rt, ok := runnerTools[name]
		require.True(t, ok, "runner missing relay tool %s", name)
		st, ok := stdioTools[name]
		require.True(t, ok, "stdio missing tool %s", name)
		assert.Equal(t, st.Description, rt.Description, "%s description drifted between surfaces", name)
	}
}

// TestStdioServer_EveryToolClassified: the legacy stdio surface (bare-mcp
// fallback, retained unchanged) exposes no tool the routing table does not
// classify.
func TestStdioServer_EveryToolClassified(t *testing.T) {
	s := &ctxServer{cfg: testConfig()}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)
	tools := listServerTools(t, server)
	require.NotEmpty(t, tools)

	routes := mcpschema.Routes()
	for name := range tools {
		_, ok := routes[name]
		assert.True(t, ok, "stdio tool %q is unclassified — add it to mcpschema.Routes", name)
	}
}
