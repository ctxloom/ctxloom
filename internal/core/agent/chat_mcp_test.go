package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// ctxloomBundleServer is the entry ctxloom's own companion loadout contributes
// to a resolved server set: the bare binary name and the `mcp serve` leaf,
// written to every surface as declared.
func ctxloomBundleServer() wire.MCPServer {
	return wire.MCPServer{ServedBy: wire.ServedBySessionEndpoint}
}

// TestComposeChatMCPServers_ManagedSet: the composed chat set carries the SAME
// source the settings writers reconcile — the resolved bundle servers,
// ctxloom's own (from the builtin ctxloom bundle) included — name-sorted for a
// deterministic frame.
func TestComposeChatMCPServers_ManagedSet(t *testing.T) {
	bundle := map[string]wire.MCPServer{
		MCPServerName: ctxloomBundleServer(),
		"taskloom":    {Command: "taskloom", Args: []string{"mcp"}},
		"tools":       {Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}},
	}

	got := ComposeChatMCPServers(bundle, nil)

	require.Len(t, got, 3)
	assert.Equal(t, ChatMCPServer{Name: MCPServerName, Transport: MCPTransportHTTP}, got[0],
		"ctxloom's session-endpoint declaration composes as a name-only http server: the URL is the session's, rendered at delivery")
	assert.Equal(t, ChatMCPServer{Name: "taskloom", Command: "taskloom", Args: []string{"mcp"}}, got[1])
	assert.Equal(t, ChatMCPServer{Name: "tools", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}}, got[2])
}

// TestComposeChatMCPServers_RemoteServer: a URL-bearing wire entry composes
// as an http-transport chat server — the discriminator is DERIVED from the
// URL here, never stored on the wire type — carrying URL and Headers and no
// command. The stdio arm is unaffected.
func TestComposeChatMCPServers_RemoteServer(t *testing.T) {
	bundle := map[string]wire.MCPServer{
		"remote": {URL: "https://mcp.example.com/v1", Headers: map[string]string{"Authorization": "Bearer t"}},
		"stdio":  {Command: "cmd", Args: []string{"a"}},
	}

	got := ComposeChatMCPServers(bundle, nil)

	require.Len(t, got, 2)
	assert.Equal(t, ChatMCPServer{
		Name:      "remote",
		Transport: MCPTransportHTTP,
		URL:       "https://mcp.example.com/v1",
		Headers:   map[string]string{"Authorization": "Bearer t"},
	}, got[0])
	assert.Equal(t, ChatMCPServer{Name: "stdio", Command: "cmd", Args: []string{"a"}}, got[1])
}

// TestComposeChatMCPServers_CtxloomWithheld: a resolved set that carries no
// ctxloom entry — the builtin bundle's server withheld by a profile's
// exclude_mcp or by rejection — composes without one, exactly like the
// settings write.
func TestComposeChatMCPServers_CtxloomWithheld(t *testing.T) {
	got := ComposeChatMCPServers(map[string]wire.MCPServer{
		"taskloom": {Command: "taskloom", Args: []string{"mcp"}},
	}, nil)

	require.Len(t, got, 1, "the withheld ctxloom entry must not be re-added by the composer")
	assert.Equal(t, "taskloom", got[0].Name)
}

// TestComposeChatMCPServers_ExistingNameWins: a caller-supplied server with a
// managed name suppresses the managed entry — one server per name, the
// explicit entry wins.
func TestComposeChatMCPServers_ExistingNameWins(t *testing.T) {
	bundle := map[string]wire.MCPServer{
		MCPServerName: ctxloomBundleServer(),
		"taskloom":    {Command: "taskloom", Args: []string{"mcp"}},
	}

	got := ComposeChatMCPServers(bundle,
		[]ChatMCPServer{{Name: MCPServerName, Command: "/custom/ctxloom"}})

	require.Len(t, got, 1)
	assert.Equal(t, "taskloom", got[0].Name)
}

// TestComposeChatMCPServers_NoManagedPayload: a nil bundle set means no managed
// payload was assembled (the minimal form / a failed config load) — nothing is
// injected, mirroring the lifecycle Flush no-op.
func TestComposeChatMCPServers_NoManagedPayload(t *testing.T) {
	assert.Nil(t, ComposeChatMCPServers(nil, nil))
}

// TestComposeChatMCPServers_SessionEndpointDeclaration pins what the
// composer makes of ctxloom's own entry: a name-only http server. The journal
// records NAMES; a file writer handed this shape refuses it
// (ChatMCPConfigEntryOf, wire.ErrMCPServerUnrendered) because the URL is the
// session's and only the engine's dynamic approach renders it.
func TestComposeChatMCPServers_SessionEndpointDeclaration(t *testing.T) {
	got := ComposeChatMCPServers(map[string]wire.MCPServer{MCPServerName: ctxloomBundleServer()}, nil)
	require.Len(t, got, 1)
	assert.Equal(t, MCPTransportHTTP, got[0].Transport)
	assert.Empty(t, got[0].Command, "nothing executable")
	assert.Empty(t, got[0].URL, "the URL is the session's, not the declaration's")

	_, err := ChatMCPConfigEntryOf(got[0])
	assert.ErrorIs(t, err, wire.ErrMCPServerUnrendered, "a writer refuses the unrendered declaration")
}

// TestManagedConfigChatMCPServers: the ManagedConfig-shaped entry point — the
// structured run path composes from the SAME payload RunStart ships to Setup;
// a nil managed payload injects nothing.
func TestManagedConfigChatMCPServers(t *testing.T) {
	var nilManaged *ManagedConfig
	assert.Nil(t, nilManaged.ChatMCPServers())

	m := &ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{
			MCPServerName: ctxloomBundleServer(),
			"taskloom":    {Command: "taskloom", Args: []string{"mcp"}},
		},
	}
	got := m.ChatMCPServers()
	require.Len(t, got, 2)
	assert.Equal(t, MCPServerName, got[0].Name)
	assert.Equal(t, "taskloom", got[1].Name)
}

// TestBaseLifecycle_ChatMCPServers: the lifecycle composes from its merged
// managed payload; one that never saw MergeManaged (the minimal form) yields nil.
func TestBaseLifecycle_ChatMCPServers(t *testing.T) {
	l := NewBaseLifecycle("acp")
	assert.Nil(t, l.ChatMCPServers(), "no managed payload merged → nothing to inject")

	l.MergeManaged(termRep(), &ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{
			MCPServerName: ctxloomBundleServer(),
			"taskloom":    {Command: "taskloom", Args: []string{"mcp"}},
		},
	}, "/work", "")

	got := l.ChatMCPServers()
	require.Len(t, got, 2)
	assert.Equal(t, MCPServerName, got[0].Name)
	assert.Equal(t, "taskloom", got[1].Name)
}

// TestComposeChatMCPServers_UncoveredArms covers the arms the tests above
// leave alone: the empty-set and fully-suppressed returns must be nil rather
// than an empty slice, matching the no-payload return.
func TestComposeChatMCPServers_UncoveredArms(t *testing.T) {
	t.Run("empty merged set returns nil, not an empty slice", func(t *testing.T) {
		assert.Nil(t, ComposeChatMCPServers(map[string]wire.MCPServer{}, nil),
			"nothing to inject must be nil, matching the no-payload return")
	})

	t.Run("existing entries can empty the set completely", func(t *testing.T) {
		got := ComposeChatMCPServers(map[string]wire.MCPServer{MCPServerName: ctxloomBundleServer()},
			[]ChatMCPServer{{Name: MCPServerName}})
		assert.Nil(t, got, "the caller's own entry wins and nothing is left to add")
	})
}
