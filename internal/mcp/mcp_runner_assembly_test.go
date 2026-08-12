package mcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/mcpschema"
)

// Characterization coverage for newRunnerMCPServer, written BEFORE its
// complexity split so the split is provably behaviour-preserving. A pure
// complexity reduction cannot have a red — behaviour is unchanged by definition
// — so these must be green before and after, and they cover the arms the
// existing runner tests leave to inference: that the cell-local RESOURCE
// surface is registered alongside the tools, and that the docgen path (nil
// config, empty harp) assembles the same tool set as a live one.

// listServerResources connects an in-memory client and lists the server's
// resources, the resource-side twin of listServerTools.
func listServerResources(t *testing.T, server *mcp.Server) map[string]*mcp.Resource {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), st, nil)
	require.NoError(t, err)
	defer func() { _ = ss.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), ct, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	out := map[string]*mcp.Resource{}
	cursor := ""
	for {
		page, err := cs.ListResources(context.Background(), &mcp.ListResourcesParams{Cursor: cursor})
		require.NoError(t, err)
		for _, r := range page.Resources {
			out[r.URI] = r
		}
		if page.NextCursor == "" {
			return out
		}
		cursor = page.NextCursor
	}
}

// TestRunnerServer_RegistersTheCellLocalResourceSurface: the runner serves
// resources as well as tools, and nothing in the runner tests asserted it — a
// registration reordering could have dropped the whole resource surface with
// every tool assertion still green.
func TestRunnerServer_RegistersTheCellLocalResourceSurface(t *testing.T) {
	server, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)

	got := listServerResources(t, server)
	for _, uri := range []string{
		resourceHelpURI,
		resourceSessionsRecentURI,
		resourceFragmentsURI,
		resourceProfilesURI,
		resourcePromptsURI,
		resourceSkillsURI,
		resourceRemotesURI,
		resourceMCPServersURI,
		resourceSessionsURI,
	} {
		assert.Contains(t, got, uri, "the runner must serve %s", uri)
	}
}

// TestRunnerServer_DocgenPathAssemblesTheSameTools: NewDocMCPServer builds the
// surface with a nil config and an empty harp, and the generated reference page
// is what a reader is told the runner serves. The two must not diverge.
func TestRunnerServer_DocgenPathAssemblesTheSameTools(t *testing.T) {
	live, err := newRunnerMCPServer(testConfig(), "test-harp", testHome(t), false, "")
	require.NoError(t, err)
	docs, err := newRunnerMCPServer(nil, "", testHome(t), false, "")
	require.NoError(t, err)

	liveNames := make([]string, 0)
	for name := range listServerTools(t, live) {
		liveNames = append(liveNames, name)
	}
	docNames := make([]string, 0)
	for name := range listServerTools(t, docs) {
		docNames = append(docNames, name)
	}
	assert.ElementsMatch(t, liveNames, docNames,
		"the documented surface must be the surface a live runner serves")
}

// TestClaimRoutes_RejectsAMisclassifiedTool covers the arm the split finally
// made reachable. Before it, "this tool is served here but classified
// elsewhere" lived inline in newRunnerMCPServer behind mcpschema.Routes, which
// no test can perturb — so the one check that stops a tool being served by the
// wrong route had no coverage at all.
func TestClaimRoutes_RejectsAMisclassifiedTool(t *testing.T) {
	routes := map[string]mcpschema.Route{
		"good": mcpschema.RouteCellLocal,
		"bad":  mcpschema.RouteHostRelay,
	}

	registered := map[string]bool{}
	require.NoError(t, claimRoutes(routes, registered, mcpschema.RouteCellLocal, "good"))
	assert.True(t, registered["good"], "a matching tool is marked served")

	err := claimRoutes(routes, registered, mcpschema.RouteCellLocal, "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bad"`, "the failure must name the tool")
	assert.Contains(t, err.Error(), "cell-local", "and the route it was served on, in words")
	assert.False(t, registered["bad"], "a misclassified tool must not count as served")

	// An unclassified name is a mismatch too: routes[""] is the zero Route,
	// which is RouteCoordination, so a missing entry must never silently
	// satisfy a coordination claim it was never given.
	require.Error(t, claimRoutes(routes, map[string]bool{}, mcpschema.RouteCellLocal, "absent"))
}

// TestGeneratedToolHandler_RefusesAnUnclassifiedRoute pins the other newly
// reachable arm: an unclassified generated tool must fail runner startup, never
// fall through to a nil handler.
func TestGeneratedToolHandler_RefusesAnUnclassifiedRoute(t *testing.T) {
	home := testHome(t)

	h, err := generatedToolHandler(home, "harp", t.TempDir(), mcpschema.RouteCellLocal, "agent_run")
	assert.Nil(t, h)
	require.Error(t, err, "cell-local is not a generated-tool route")
	assert.Contains(t, err.Error(), "agent_run")

	h, err = generatedToolHandler(home, "harp", t.TempDir(), mcpschema.RouteCoordination, "agent_run")
	require.NoError(t, err)
	assert.NotNil(t, h)
}

// TestRouteName_NamesEveryRoute: the enum is an int, so an unnamed member would
// print as a bare number in the one error a runner dies on.
func TestRouteName_NamesEveryRoute(t *testing.T) {
	for _, r := range []mcpschema.Route{
		mcpschema.RouteCoordination,
		mcpschema.RouteCellLocal,
		mcpschema.RouteHostRelay,
		mcpschema.RouteArtifactFetch,
	} {
		name := routeName(r)
		assert.NotEmpty(t, name)
		assert.NotContains(t, name, "route(", "route %d has no name", int(r))
	}
}
