package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// ctxloomBundleServer is the entry the builtin ctxloom bundle contributes to a
// resolved server set: the bare binary name and the `mcp serve` leaf, which
// ResolveManagedMCPServers reconstructs from ctxloom's own values.
func ctxloomBundleServer() wire.MCPServer {
	return wire.MCPServer{Command: CtxloomBinary, Args: []string{"mcp", "serve"}}
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
	assert.Equal(t, ChatMCPServer{Name: MCPServerName, Command: CtxloomCommand(), Args: CtxloomMCPArgs}, got[0])
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

// TestComposeChatMCPServers_BareCommand pins the portability invariant at the
// structured-chat composer: ctxloom's own entry names the BARE executable and
// nothing machine-specific, so the same composed set is exec'able on any host
// and inside any container. A self-exec absolute path here is the defect.
func TestComposeChatMCPServers_BareCommand(t *testing.T) {
	got := ComposeChatMCPServers(map[string]wire.MCPServer{MCPServerName: ctxloomBundleServer()}, nil)
	require.Len(t, got, 1)
	assert.Equal(t, "ctxloom", got[0].Command,
		"the composed ctxloom command must be the bare name, resolved on PATH at fire time")
	assert.Equal(t, CtxloomMCPArgs, got[0].Args)
}

// TestResolveManagedMCPServers pins the split the builtin bundle depends on:
// the bundle says WHETHER ctxloom's own server is registered, this function
// fixes WHAT is written, and it never mutates the caller's map (one resolved
// set is shared across engines and cells).
func TestResolveManagedMCPServers(t *testing.T) {
	// A deliberately WRONG invocation under ctxloom's own name: the source's
	// command and args must both be discarded and rebuilt, so neither can be
	// mistaken for a pass-through.
	src := map[string]wire.MCPServer{
		MCPServerName: {Command: "/bundle/declared/ctxloom", Args: []string{"bogus"}},
		"other":       {Command: "other", Args: []string{"x"}},
	}

	out, _ := ResolveManagedMCPServers(src)

	assert.Equal(t, "ctxloom", out[MCPServerName].Command)
	assert.Equal(t, CtxloomMCPArgs, out[MCPServerName].Args)
	assert.Equal(t, wire.MCPServer{Command: "other", Args: []string{"x"}}, out["other"],
		"every other entry passes through untouched")
	assert.Equal(t, "/bundle/declared/ctxloom", src[MCPServerName].Command,
		"the caller's map must not be mutated")

	withheld := map[string]wire.MCPServer{"other": {Command: "other"}}
	assert.Equal(t, withheld, resolvedManaged(withheld),
		"a set with no ctxloom entry is returned unchanged — nothing invents one")
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

	l.MergeManaged(&ManagedConfig{
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
