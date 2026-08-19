package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The MCP-server listing reads the set a session actually registers
// (Config.ResolveBundleMCPServers), so every entry it returns is a bundle item.
// These tests assert on the ONE server every project gets — ctxloom's own, from
// the builtin ctxloom bundle — because that is the entry whose absence costs
// the user every ctxloom tool. Nothing here configures a server: if the
// builtin's unconditional injection stopped delivering MCP, every one of them
// goes red.

func TestListMCPServers_ReturnsCtxloomsOwnServerResolved(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	res, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{})
	require.NoError(t, err)

	var own *MCPServerEntry
	for i := range res.Servers {
		if res.Servers[i].Name == agent.MCPServerName {
			own = &res.Servers[i]
		}
	}
	require.NotNil(t, own, "ctxloom's own MCP server must be listed; got %+v", res.Servers)
	assert.Equal(t, res.Count, len(res.Servers), "Count must match the slice it describes")
	assert.Equal(t, "ctxloom+builtin:ctxloom-mcp", own.Source,
		"the listing must name the bundle the server came from, with the bundle: prefix stripped")
	assert.Equal(t, agent.CtxloomMCPArgs, own.Args, "the listed entry must invoke the `mcp serve` leaf")
	// The listing must report the command a SETTINGS FILE would receive, which
	// is agent.CtxloomCommand's self-exec resolution of the bare name the bundle
	// declares. (Under `go test` self-lookup falls back to the bare name, so the
	// two coincide here; agent.TestResolveManagedMCPServers is where the
	// substitution itself is pinned.)
	assert.Equal(t, agent.CtxloomCommand(), own.Command)
}

func TestGetMCPServer_FindsCtxloomsOwnServer(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	res, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: agent.MCPServerName})
	require.NoError(t, err)
	require.True(t, res.Found)
	require.Len(t, res.Entries, 1, "one name resolves to one server")
	assert.Equal(t, agent.MCPServerName, res.Entries[0].Name)
	assert.Equal(t, "ctxloom+builtin:ctxloom-mcp", res.Entries[0].Source)
}

func TestGetMCPServer_NotFound(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	res, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: "no-such-server"})
	require.NoError(t, err)
	assert.False(t, res.Found)
	assert.Empty(t, res.Entries)
	assert.NotNil(t, res.Entries, "Entries is never nil, so a json consumer always reads a list")
}

func TestListMCPServers_QueryFiltersByNameAndCommand(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{})

	all, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{})
	require.NoError(t, err)
	require.NotZero(t, all.Count, "the fixture must resolve at least ctxloom's own server before filtering means anything")

	byName, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{Query: "CTXLOOM"})
	require.NoError(t, err)
	assert.NotZero(t, byName.Count, "the query is case-insensitive over the name")

	none, err := ListMCPServers(context.Background(), cfg, ListMCPServersRequest{Query: "zzz-no-such-server"})
	require.NoError(t, err)
	assert.Zero(t, none.Count)
}

func TestSortMCPServers_UnknownKeySortsByNameLoudly(t *testing.T) {
	servers := []MCPServerEntry{
		{Name: "beta", Command: "a-cmd"},
		{Name: "alpha", Command: "z-cmd"},
	}

	sortMCPServers(servers, "nonsense", "")
	assert.Equal(t, "alpha", servers[0].Name, "an unrecognised sort key must still produce a deterministic order")

	sortMCPServers(servers, "command", "")
	assert.Equal(t, "beta", servers[0].Name, "sort_by=command orders on the command")

	sortMCPServers(servers, "name", "desc")
	assert.Equal(t, "beta", servers[0].Name, "desc reverses the order")
}
