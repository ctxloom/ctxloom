package cli

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterization coverage for newRunnerMCPServer, written BEFORE the U038-F08
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
