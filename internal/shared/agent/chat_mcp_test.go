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

	got := ComposeChatMCPServers("claude-code", "", mcp, bundle, nil)

	require.Len(t, got, 3)
	assert.Equal(t, ChatMCPServer{Name: MCPServerName, Command: CtxloomCommand(), Args: CtxloomMCPArgs}, got[0])
	assert.Equal(t, ChatMCPServer{Name: "taskloom", Command: "taskloom", Args: []string{"mcp"}}, got[1])
	assert.Equal(t, ChatMCPServer{Name: "tools", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"A": "1"}}, got[2])
}

// TestComposeChatMCPServers_AutoRegisterOff: auto_register_ctxloom: false
// suppresses the ctxloom entry, exactly like the settings write.
func TestComposeChatMCPServers_AutoRegisterOff(t *testing.T) {
	off := false
	assert.Nil(t, ComposeChatMCPServers("acp", "", &wire.MCPConfig{AutoRegisterCtxloom: &off}, nil, nil))
}

// TestComposeChatMCPServers_ExistingNameWins: a caller-supplied server with a
// managed name suppresses the managed entry — one server per name, the
// explicit entry wins.
func TestComposeChatMCPServers_ExistingNameWins(t *testing.T) {
	bundle := map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}}

	got := ComposeChatMCPServers("acp", "", &wire.MCPConfig{}, bundle,
		[]ChatMCPServer{{Name: MCPServerName, Command: "/custom/ctxloom"}})

	require.Len(t, got, 1)
	assert.Equal(t, "taskloom", got[0].Name)
}

// TestComposeChatMCPServers_NoManagedPayload: nil mcp AND nil bundle set means
// no managed payload was assembled (skip-setup / failed config load) — nothing
// is injected, mirroring the lifecycle Flush no-op.
func TestComposeChatMCPServers_NoManagedPayload(t *testing.T) {
	assert.Nil(t, ComposeChatMCPServers("acp", "", nil, nil, nil))
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

	got := ComposeChatMCPServers("claude-code", "", mcp, bundle, nil)

	byName := make(map[string]ChatMCPServer, len(got))
	for _, s := range got {
		byName[s.Name] = s
	}
	assert.Equal(t, "config-wins", byName["dup"].Command)
	assert.Contains(t, byName, "native")
	assert.NotContains(t, byName, "other")
}

// TestComposeChatMCPServers_CommandOverride pins the fix for the
// structured-chat MCP path, which used to hardcode CtxloomCommand() (the HOST
// self-exec absolute path) instead of honoring a command override, so a
// runtime:container agent's structured chat baked the host's binary path into
// its own mcpServers instead of the in-container path Setup's settings write
// already uses (ResolveMCPCommand) — the engine's `ctxloom mcp` stdio shim
// then never launches inside the container.
func TestComposeChatMCPServers_CommandOverride(t *testing.T) {
	got := ComposeChatMCPServers("claude-code", "/in-container/ctxloom", &wire.MCPConfig{}, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, "/in-container/ctxloom", got[0].Command,
		"a non-empty override must win over the host self-exec-absolute default, matching ResolveMCPCommand")

	// Empty override is a no-op — the host self-exec-absolute invariant is untouched.
	got = ComposeChatMCPServers("claude-code", "", &wire.MCPConfig{}, nil, nil)
	require.Len(t, got, 1)
	assert.Equal(t, CtxloomCommand(), got[0].Command)
}

// TestPatchManagedCommand pins the coordinator-delegation fix at the unit
// hop: a caller that had to compose the managed set BEFORE the
// isolation policy (and thus the override) was known can patch the
// auto-registered ctxloom entry's Command afterward, without disturbing any
// other entry — and without the empty-override / no-matching-entry cases
// mutating anything.
func TestPatchManagedCommand(t *testing.T) {
	servers := []ChatMCPServer{
		{Name: MCPServerName, Command: CtxloomCommand(), Args: CtxloomMCPArgs},
		{Name: "other", Command: "/usr/local/bin/other-tool"},
	}

	patched := PatchManagedCommand(servers, "/in-container/ctxloom")
	require.Len(t, patched, 2)
	assert.Equal(t, "/in-container/ctxloom", patched[0].Command,
		"the auto-registered ctxloom entry's command must be patched")
	assert.Equal(t, "/usr/local/bin/other-tool", patched[1].Command,
		"a non-ctxloom entry must be left untouched")
	assert.Equal(t, CtxloomCommand(), servers[0].Command,
		"the original slice's entry must not be mutated in place")

	assert.Equal(t, servers, PatchManagedCommand(servers, ""),
		"an empty override is a no-op")

	noManaged := []ChatMCPServer{{Name: "other", Command: "/usr/local/bin/other-tool"}}
	assert.Equal(t, noManaged, PatchManagedCommand(noManaged, "/in-container/ctxloom"),
		"a non-empty override with no matching entry is a no-op")
}

// TestManagedConfigChatMCPServers: the ManagedConfig-shaped entry point — the
// structured run path composes from the SAME payload RunStart ships to Setup;
// a nil managed payload injects nothing.
func TestManagedConfigChatMCPServers(t *testing.T) {
	var nilManaged *ManagedConfig
	assert.Nil(t, nilManaged.ChatMCPServers("claude-code", ""))

	m := &ManagedConfig{
		MCP:       &wire.MCPConfig{},
		BundleMCP: map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}},
	}
	got := m.ChatMCPServers("claude-code", "")
	require.Len(t, got, 2)
	assert.Equal(t, MCPServerName, got[0].Name)
	assert.Equal(t, "taskloom", got[1].Name)
}

// TestBaseLifecycle_ChatMCPServers: the lifecycle composes from its merged
// managed payload; one that never saw MergeManaged (skip-setup) yields nil.
func TestBaseLifecycle_ChatMCPServers(t *testing.T) {
	l := NewBaseLifecycle("acp")
	assert.Nil(t, l.ChatMCPServers(""), "no managed payload merged → nothing to inject")

	l.MergeManaged(&ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{"taskloom": {Command: "taskloom", Args: []string{"mcp"}}},
	}, "/work", "")

	got := l.ChatMCPServers("")
	require.Len(t, got, 2)
	assert.Equal(t, MCPServerName, got[0].Name)
	assert.Equal(t, "taskloom", got[1].Name)
}

// CHARACTERIZATION for the complexity split: the arms the existing tests
// leave uncovered, so the decomposition is provably behaviour-preserving rather
// than merely green on the paths that were already exercised. A pure complexity
// reduction changes no behaviour by definition, so no test can go red for it —
// these are the "green before and after" half of that contract.
func TestComposeChatMCPServers_UncoveredArms(t *testing.T) {
	off := false

	t.Run("nil mcp with bundle servers still composes", func(t *testing.T) {
		// mcp is nil while bundleMCP is not: the guard lets this through and the
		// auto-register probe runs on a nil *wire.MCPConfig (nil-safe by
		// contract — wire.MCPConfig.ShouldAutoRegisterCtxloom returns true).
		got := ComposeChatMCPServers("claude", "", nil,
			map[string]wire.MCPServer{"bundled": {Command: "b", Args: []string{"x"}}}, nil)
		require.Len(t, got, 2, "the auto-registered ctxloom server plus the bundle server")
		assert.Equal(t, "bundled", got[0].Name, "the result is name-sorted")
		assert.Equal(t, MCPServerName, got[1].Name)
	})

	t.Run("empty merged set returns nil, not an empty slice", func(t *testing.T) {
		got := ComposeChatMCPServers("claude", "",
			&wire.MCPConfig{AutoRegisterCtxloom: &off}, map[string]wire.MCPServer{}, nil)
		assert.Nil(t, got, "nothing to inject must be nil, matching the no-payload return")
	})

	t.Run("existing entries can empty the set completely", func(t *testing.T) {
		got := ComposeChatMCPServers("claude", "", &wire.MCPConfig{}, nil,
			[]ChatMCPServer{{Name: MCPServerName}})
		assert.Nil(t, got, "the caller's own entry wins and nothing is left to add")
	})

	t.Run("plugin servers for a DIFFERENT engine key are ignored", func(t *testing.T) {
		got := ComposeChatMCPServers("claude", "", &wire.MCPConfig{
			AutoRegisterCtxloom: &off,
			Plugins: map[string]map[string]wire.MCPServer{
				"codex": {"codex-only": {Command: "c"}},
			},
		}, nil, nil)
		assert.Nil(t, got, "another engine's passthrough servers are not this engine's")
	})

	t.Run("plugin servers override unified servers of the same name", func(t *testing.T) {
		got := ComposeChatMCPServers("claude", "", &wire.MCPConfig{
			AutoRegisterCtxloom: &off,
			Servers:             map[string]wire.MCPServer{"dup": {Command: "unified"}},
			Plugins: map[string]map[string]wire.MCPServer{
				"claude": {"dup": {Command: "plugin"}},
			},
		}, nil, nil)
		require.Len(t, got, 1)
		assert.Equal(t, "plugin", got[0].Command, "the engine's own passthrough wins, as documented")
	})
}
