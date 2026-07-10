package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// TestComposeChatMCPServers_ManagedSet: the composed chat set carries the SAME
// sources the settings writers reconcile — the auto-registered ctxloom server,
// bundle-shipped servers (taskloom rides the builtin bundle), and the
// config/profile servers — name-sorted for a deterministic frame.
func TestComposeChatMCPServers_ManagedSet(t *testing.T) {
	mcp := &wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"tools": {Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}},
	}}
	bundle := map[string]wire.MCPServer{
		"taskloom": {Command: "taskloom", Args: []string{"mcp"}},
	}

	got := ComposeChatMCPServers("claude-code", mcp, bundle, nil)

	require.Len(t, got, 3)
	assert.Equal(t, ChatMCPServer{Name: MCPServerName, Command: CtxloomCommand(), Args: CtxloomMCPArgs}, got[0])
	assert.Equal(t, ChatMCPServer{Name: "taskloom", Command: "taskloom", Args: []string{"mcp"}}, got[1])
	assert.Equal(t, ChatMCPServer{Name: "tools", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}}, got[2])
}

// TestComposeChatMCPServers_AutoRegisterOff: auto_register_ctxloom: false
// suppresses the ctxloom entry, exactly like the settings write.
func TestComposeChatMCPServers_AutoRegisterOff(t *testing.T) {
	off := false
	assert.Nil(t, ComposeChatMCPServers("acp", &wire.MCPConfig{AutoRegisterCtxloom: &off}, nil, nil))
}

// TestComposeChatMCPServers_ExistingNameWins: a caller-supplied server with a
// managed name suppresses the managed entry — one server per name, the
// explicit entry wins.
func TestComposeChatMCPServers_ExistingNameWins(t *testing.T) {
	bundle := map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}}

	got := ComposeChatMCPServers("acp", &wire.MCPConfig{}, bundle,
		[]ChatMCPServer{{Name: MCPServerName, Command: "/custom/ctxloom"}})

	require.Len(t, got, 1)
	assert.Equal(t, "taskloom", got[0].Name)
}

// TestComposeChatMCPServers_NoManagedPayload: nil mcp AND nil bundle set means
// no managed payload was assembled (skip-setup / failed config load) — nothing
// is injected, mirroring the lifecycle Flush no-op.
func TestComposeChatMCPServers_NoManagedPayload(t *testing.T) {
	assert.Nil(t, ComposeChatMCPServers("acp", nil, nil, nil))
}

// TestComposeChatMCPServers_OverrideOrder: config servers override same-name
// bundle servers, and only the caller's own plugin key passes through —
// matching the settings writers' write order.
func TestComposeChatMCPServers_OverrideOrder(t *testing.T) {
	mcp := &wire.MCPConfig{
		Servers: map[string]wire.MCPServer{"dup": {Command: "config-wins"}},
		Plugins: map[string]map[string]wire.MCPServer{
			"claude-code": {"native": {Command: "native-cmd"}},
			"codex":       {"other": {Command: "other-cmd"}},
		},
	}
	bundle := map[string]wire.MCPServer{"dup": {Command: "bundle-loses"}}

	got := ComposeChatMCPServers("claude-code", mcp, bundle, nil)

	byName := make(map[string]ChatMCPServer, len(got))
	for _, s := range got {
		byName[s.Name] = s
	}
	assert.Equal(t, "config-wins", byName["dup"].Command)
	assert.Contains(t, byName, "native")
	assert.NotContains(t, byName, "other")
}

// TestManagedConfigChatMCPServers: the ManagedConfig-shaped entry point — the
// structured run path composes from the SAME payload RunStart ships to Setup;
// a nil managed payload injects nothing.
func TestManagedConfigChatMCPServers(t *testing.T) {
	var nilManaged *ManagedConfig
	assert.Nil(t, nilManaged.ChatMCPServers("claude-code"))

	m := &ManagedConfig{
		MCP:       &wire.MCPConfig{},
		BundleMCP: map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}},
	}
	got := m.ChatMCPServers("claude-code")
	require.Len(t, got, 2)
	assert.Equal(t, MCPServerName, got[0].Name)
	assert.Equal(t, "taskloom", got[1].Name)
}

// TestBaseLifecycle_ChatMCPServers: the lifecycle composes from its merged
// managed payload; one that never saw MergeManaged (skip-setup) yields nil.
func TestBaseLifecycle_ChatMCPServers(t *testing.T) {
	l := NewBaseLifecycle("acp")
	assert.Nil(t, l.ChatMCPServers(), "no managed payload merged → nothing to inject")

	l.MergeManaged(&ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}},
	}, "/work", "")

	got := l.ChatMCPServers()
	require.Len(t, got, 2)
	assert.Equal(t, MCPServerName, got[0].Name)
	assert.Equal(t, "taskloom", got[1].Name)
}
