package mcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The stdio server against the session endpoint's surface: the two must
// describe the tools they share identically, and the stdio server must
// advertise every tool the routing table makes it responsible for.

func testConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		LM: config.LMConfig{Configs: map[string]config.LLMConfig{}},
	})
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

// runnerSurface is the session endpoint's surface over this package's
// config-backed local half, as the owner arm serves it.
func runnerSurface(t *testing.T) *mcp.Server {
	t.Helper()
	server, err := runnermcp.NewServer(report.To(nil), testHome(t), "test-harp", "/work", false, configSurface{s: &ctxServer{cfg: testConfig()}})
	require.NoError(t, err)
	return server
}

// TestRunnerServer_HostRelayDescriptionsMatchStdio pins the description
// parity between the runner's host-relay registrations and the stdio
// server's typed ones — the two surfaces must not drift.
func TestRunnerServer_HostRelayDescriptionsMatchStdio(t *testing.T) {
	runnerTools := listServerTools(t, runnerSurface(t))

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

// TestStdioServer_AdvertisesEveryLocallyServedTool closes the gap that let a
// tool ship "green" while absent from the wire: the checks below only ever
// asserted stdio tools ⊆ routes (nothing UNCLASSIFIED), never routes ⊆ stdio
// tools (nothing MISSING). A tool whose registration was dropped — or whose
// schema inference failed such that the SDK never advertised it — would have
// passed every existing test.
//
// Coordination + artifact-fetch tools are runner-only (generated, proto-
// canonical), so the stdio surface is asserted against exactly the routes it
// is responsible for: cell-local and host-relay.
func TestStdioServer_AdvertisesEveryLocallyServedTool(t *testing.T) {
	s := &ctxServer{cfg: testConfig()}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)
	tools := listServerTools(t, server)

	for name, route := range mcpschema.Routes() {
		if route != mcpschema.RouteCellLocal && route != mcpschema.RouteHostRelay {
			continue
		}
		_, ok := tools[name]
		assert.True(t, ok, "classified tool %q is NOT advertised by the stdio server's tools/list — it was registered but never served", name)
	}

	// Named explicitly: this is the tool whose advertisement regressed.
	_, ok := tools["evaluate_triggers"]
	assert.True(t, ok, "evaluate_triggers must appear in the stdio server's advertised tools")
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
