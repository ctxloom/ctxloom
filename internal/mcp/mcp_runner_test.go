package mcp

import (
	"context"
	"encoding/json"
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
	server, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t), false, "")
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
	server, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t), false, "")
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
	runner, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t), false, "")
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

// TestRunnerServer_AdvertisesEvaluateTriggers pins the same advertisement on
// the runner surface (where a real harness actually reaches it).
func TestRunnerServer_AdvertisesEvaluateTriggers(t *testing.T) {
	server, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)
	tools := listServerTools(t, server)
	_, ok := tools["evaluate_triggers"]
	assert.True(t, ok, "evaluate_triggers must appear in the runner server's advertised tools")
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

// approvalDecisionVocabulary is ApprovalDecision.Decision's declared,
// non-zero vocabulary (coordination.proto) — the exact set a coordinator
// answering a relayed approval_request may send, and the set the reply
// decoder (coord.decisionFromStructured) enforces.
var approvalDecisionVocabulary = []string{
	"DECISION_ACCEPT",
	"DECISION_ACCEPT_FOR_SESSION",
	"DECISION_DECLINE",
	"DECISION_CANCEL",
}

// TestAgentSend_DocumentsApprovalDecisionVocabularyOnBothSurfaces closes
// modal-chess. agent_send is the ONLY way to answer a relayed approval, and
// its `structured` argument carries the ApprovalDecision — but the vocabulary
// was documented on the stdio surface alone. The generated (proto-canonical)
// schema, which is what a real runner-hosted engine actually reads, named no
// DECISION_ value at all, so the enum was discoverable only by trial and
// error and a plausible-but-wrong guess failed silently at the far end.
//
// The two surfaces are generated independently (RouteCoordination →
// generated JSON; stdio → the typed agentSendInput struct tags), so nothing
// else makes them agree. This is that guard.
func TestAgentSend_DocumentsApprovalDecisionVocabularyOnBothSurfaces(t *testing.T) {
	generated, ok := mcpschema.ToolByName(mcpschema.ToolAgentSend)
	require.True(t, ok, "agent_send must have a generated schema")
	genJSON, err := json.Marshal(generated.InputSchema)
	require.NoError(t, err)

	s := &ctxServer{cfg: testConfig()}
	server := mcp.NewServer(&mcp.Implementation{Name: "ctxloom", Version: "test"}, nil)
	s.registerTools(server)
	stdioTool, ok := listServerTools(t, server)[mcpschema.ToolAgentSend]
	require.True(t, ok, "the stdio server must advertise agent_send")
	stdioJSON, err := json.Marshal(stdioTool.InputSchema)
	require.NoError(t, err)

	for _, surface := range []struct {
		name string
		raw  string
	}{
		{"generated (proto-canonical, runner surface)", string(genJSON)},
		{"stdio (typed struct tags)", string(stdioJSON)},
	} {
		for _, decision := range approvalDecisionVocabulary {
			assert.Contains(t, surface.raw, decision,
				"the %s agent_send schema must name %s — a coordinator cannot guess the approval vocabulary", surface.name, decision)
		}
		assert.Contains(t, surface.raw, "in_reply_to",
			"the %s agent_send schema must tie the ApprovalDecision to in_reply_to", surface.name)
	}
}
