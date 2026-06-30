package backends

import (
	"encoding/json"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctxloomManagedHooks is a hook set whose command is recognized as
// ctxloom-managed (executable token "ctxloom").
func ctxloomManagedHooks() *wire.HooksConfig {
	return &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
		},
	}
}

func TestClaudeCodeRemoveSettings_StripsManagedPreservesUser(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"

	// Seed a user-owned hook and a user-owned MCP server that ctxloom must not touch.
	require.NoError(t, fs.MkdirAll(dir+"/.claude", 0755))
	userSettings := `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"./user.sh"}]}]}}`
	require.NoError(t, afero.WriteFile(fs, dir+"/.claude/settings.json", []byte(userSettings), 0644))
	userMCP := `{"mcpServers":{"user-server":{"command":"./user-mcp"}}}`
	require.NoError(t, afero.WriteFile(fs, dir+"/.mcp.json", []byte(userMCP), 0644))

	// Wire ctxloom hooks, statusline (auto), and the auto-registered MCP server.
	require.NoError(t, WriteSettings("claude-code", ctxloomManagedHooks(), nil, nil, dir, WithSettingsFS(fs)))

	// Sanity: ctxloom is wired before removal.
	before, err := BackendStatus("claude-code", dir, WithSettingsFS(fs))
	require.NoError(t, err)
	require.True(t, before.Wired(), "ctxloom should be wired after WriteSettings")
	require.True(t, before.StatusLine)
	require.True(t, before.MCPPresent)

	require.NoError(t, RemoveSettings("claude-code", dir, WithSettingsFS(fs)))

	after, err := BackendStatus("claude-code", dir, WithSettingsFS(fs))
	require.NoError(t, err)
	assert.False(t, after.Wired(), "no ctxloom artifacts should remain")

	// The user's hook and MCP server survive.
	settings := readJSON(t, fs, dir+"/.claude/settings.json")
	assert.Contains(t, mustMarshal(t, settings), "./user.sh")
	mcp := readJSON(t, fs, dir+"/.mcp.json")
	assert.Contains(t, mustMarshal(t, mcp), "user-server")
}

func TestRemoveSettings_AbsentFilesAreNoOp(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, RemoveSettings("claude-code", "/empty", WithSettingsFS(fs)))
	require.NoError(t, RemoveSettings("antigravity", "/empty", WithSettingsFS(fs)))

	// Uninstall must never create config files.
	exists, _ := afero.Exists(fs, "/empty/.claude/settings.json")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, "/empty/.agents/hooks.json")
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, "/empty/.agents/mcp_config.json")
	assert.False(t, exists)
}

func TestAntigravityRemoveSettings_StripsManagedPreservesUser(t *testing.T) {
	fs := afero.NewMemMapFs()
	const dir = "/project"

	// Seed a user-owned MCP server in agy's dedicated mcp_config.json that
	// ctxloom must not touch.
	require.NoError(t, fs.MkdirAll(dir+"/.agents", 0755))
	userMCP := `{"mcpServers":{"user-server":{"command":"./user-mcp"}}}`
	require.NoError(t, afero.WriteFile(fs, dir+"/.agents/mcp_config.json", []byte(userMCP), 0644))

	require.NoError(t, WriteSettings("antigravity", ctxloomManagedHooks(), nil, nil, dir, WithSettingsFS(fs)))

	before, err := BackendStatus("antigravity", dir, WithSettingsFS(fs))
	require.NoError(t, err)
	require.True(t, before.HooksPresent, "ctxloom hooks should be wired")

	require.NoError(t, RemoveSettings("antigravity", dir, WithSettingsFS(fs)))

	after, err := BackendStatus("antigravity", dir, WithSettingsFS(fs))
	require.NoError(t, err)
	assert.False(t, after.Wired())

	mcp := readJSON(t, fs, dir+"/.agents/mcp_config.json")
	assert.Contains(t, mustMarshal(t, mcp), "user-server")
}

func TestBackendStatus_UnsupportedBackendIsUnwired(t *testing.T) {
	status, err := BackendStatus("unknown-backend", "/project")
	require.NoError(t, err)
	assert.False(t, status.Wired())
	assert.False(t, status.SettingsExists)
}

func readJSON(t *testing.T, fs afero.Fs, path string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return string(data)
}
