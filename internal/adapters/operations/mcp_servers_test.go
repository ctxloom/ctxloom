package operations

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// The MCP-server listing reads the set a session actually registers
// (Config.ResolveBundleMCPServers), so every entry it returns is a bundle item.
// These tests assert on the ONE server every project gets — ctxloom's own,
// from its own companion loadout — because that is the entry whose absence
// costs the user every ctxloom tool. Nothing here configures a server: if the
// loadout's unconditional delivery stopped registering MCP, every one of them
// goes red.

func TestListMCPServers_ReturnsCtxloomsOwnServerResolved(t *testing.T) {
	cfg := withCtxloomLoadout(t, gatedFixture(config.Fixture{}))

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
	assert.Equal(t, "ctxloom+companion:ctxloom", own.Source,
		"the listing must name the bundle the server came from, with the bundle: prefix stripped")
	assert.Equal(t, agent.CtxloomMCPArgs, own.Args, "the listed entry must invoke the `mcp serve` leaf")
	// The listing reports the command a SETTINGS FILE receives: the bare name
	// the loadout declares, written as declared (agent.CtxloomCommand's
	// invariant).
	assert.Equal(t, agent.CtxloomCommand(), own.Command)
}

func TestGetMCPServer_FindsCtxloomsOwnServer(t *testing.T) {
	cfg := withCtxloomLoadout(t, gatedFixture(config.Fixture{}))

	res, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: agent.MCPServerName})
	require.NoError(t, err)
	require.True(t, res.Found)
	require.Len(t, res.Entries, 1, "one name resolves to one server")
	assert.Equal(t, agent.MCPServerName, res.Entries[0].Name)
	assert.Equal(t, "ctxloom+companion:ctxloom", res.Entries[0].Source)
}

func TestGetMCPServer_NotFound(t *testing.T) {
	cfg := withCtxloomLoadout(t, gatedFixture(config.Fixture{}))

	res, err := GetMCPServer(context.Background(), cfg, GetMCPServerRequest{Name: "no-such-server"})
	require.NoError(t, err)
	assert.False(t, res.Found)
	assert.Empty(t, res.Entries)
	assert.NotNil(t, res.Entries, "Entries is never nil, so a json consumer always reads a list")
}

func TestListMCPServers_QueryFiltersByNameAndCommand(t *testing.T) {
	cfg := withCtxloomLoadout(t, gatedFixture(config.Fixture{}))

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

// TestMCPServerNames_EmptyIsNil proves the zero-servers case degrades to
// nil, not an empty-but-non-nil slice: a caller rendering "none" for an
// absent set must not have to distinguish the two.
func TestMCPServerNames_EmptyIsNil(t *testing.T) {
	got := MCPServerNames(nil)
	assert.Nil(t, got)
	got = MCPServerNames([]agent.ChatMCPServer{})
	assert.Nil(t, got)
}

// TestMCPServerNames_SortsRegardlessOfInputOrder proves the function
// actually SORTS (not just projects .Name) — the journaled value must be
// deterministic across runs regardless of the order servers were composed
// in (client-supplied first, then ctxloom's managed injection).
func TestMCPServerNames_SortsRegardlessOfInputOrder(t *testing.T) {
	got := MCPServerNames([]agent.ChatMCPServer{
		{Name: "zeta-server"},
		{Name: "alpha-server"},
		{Name: "mid-server"},
	})
	assert.Equal(t, []string{"alpha-server", "mid-server", "zeta-server"}, got)
}
