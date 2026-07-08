package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// fakePlacement is defined in contextdelivery_test.go (same package): a local
// agent.Placement double whose Dir() returns a fixed temp dir.

// mcpServersOf reads .mcp.json under dir and returns its mcpServers map.
func mcpServersOf(t *testing.T, dir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	require.NoError(t, err)
	var cfg map[string]any
	require.NoError(t, json.Unmarshal(data, &cfg))
	servers, _ := cfg["mcpServers"].(map[string]any)
	return servers
}

// TestFileTemplateDelivery_DeliverMCP verifies the file-template strategy writes
// the MCP surface into the injected Placement identically to writeMCPConfig, and
// that Cleanup reverts ctxloom-owned servers while preserving user servers.
func TestFileTemplateDelivery_DeliverMCP(t *testing.T) {
	mcp := &wire.MCPConfig{
		Servers: map[string]wire.MCPServer{
			"config-server": {Command: "config-cmd", Args: []string{"--flag"}},
		},
	}
	bundle := map[string]wire.MCPServer{
		"bundle-server": {Command: "bundle-cmd", SCM: "ctxloom-bundle:test"},
	}

	// Delivery target.
	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, nil)
	handle, err := d.DeliverMCP(mcp, bundle)
	require.NoError(t, err)

	// Control: the existing writer targeted at a separate dir must produce the
	// same on-disk bytes.
	controlDir := t.TempDir()
	require.NoError(t, (&ClaudeCodeHookWriter{}).writeMCPConfig(controlDir, mcp, bundle))

	got, err := os.ReadFile(filepath.Join(deliverDir, ".mcp.json"))
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(controlDir, ".mcp.json"))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "DeliverMCP must write the same .mcp.json as writeMCPConfig")

	// The ctxloom-owned servers are present after delivery.
	servers := mcpServersOf(t, deliverDir)
	assert.Contains(t, servers, "config-server")
	assert.Contains(t, servers, "bundle-server")
	assert.Contains(t, servers, AppMCPServerName)

	// Cleanup removes exactly the ctxloom-marked servers.
	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, deliverDir)
	assert.NotContains(t, servers, "config-server")
	assert.NotContains(t, servers, "bundle-server")
	assert.NotContains(t, servers, AppMCPServerName)
}

// TestFileTemplateDelivery_DeliverMCP_PreservesUserServers verifies a
// pre-existing user server (no _ctxloom marker) survives both delivery and
// cleanup while ctxloom servers come and go around it.
func TestFileTemplateDelivery_DeliverMCP_PreservesUserServers(t *testing.T) {
	dir := t.TempDir()
	existing := map[string]any{
		"mcpServers": map[string]any{
			"my-server": map[string]any{"command": "/usr/local/bin/my-mcp"},
		},
	}
	data, err := json.Marshal(existing)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".mcp.json"), data, 0o644))

	d := newFileTemplateDelivery(fakePlacement{dir: dir}, nil)
	handle, err := d.DeliverMCP(nil, nil) // nil mcp still auto-registers ctxloom
	require.NoError(t, err)

	servers := mcpServersOf(t, dir)
	assert.Contains(t, servers, "my-server")
	assert.Contains(t, servers, AppMCPServerName)

	require.NoError(t, handle.Cleanup())
	servers = mcpServersOf(t, dir)
	assert.Contains(t, servers, "my-server", "user server must survive cleanup")
	assert.NotContains(t, servers, AppMCPServerName, "ctxloom server must be reverted")
}

// TestFileTemplateDelivery_DeliverSkills verifies the skills surface is written
// into the injected Placement identically to WriteCommandFiles, and that Cleanup
// reverts the manifest-tracked set while preserving user-authored commands.
func TestFileTemplateDelivery_DeliverSkills(t *testing.T) {
	skills := []agent.CommandExport{
		{Name: "review", Content: "Review {{file}}", Enabled: true, Description: "Code review"},
		{Name: "simple", Content: "Simple command", Enabled: true},
	}

	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, nil)
	handle, err := d.DeliverSkills(skills)
	require.NoError(t, err)

	// Control: the existing writer targeted at a separate dir.
	controlDir := t.TempDir()
	require.NoError(t, WriteCommandFiles(controlDir, skills))

	for _, rel := range []string{"review.md", "simple.md", ".ctxloom-manifest"} {
		got, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "commands", rel))
		require.NoError(t, err, "DeliverSkills must write %s", rel)
		want, err := os.ReadFile(filepath.Join(controlDir, ".claude", "commands", rel))
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got), "DeliverSkills must match WriteCommandFiles for %s", rel)
	}

	// Seed a user-authored command that ctxloom does not track.
	userCmd := filepath.Join(deliverDir, ".claude", "commands", "user.md")
	require.NoError(t, os.WriteFile(userCmd, []byte("mine"), 0o644))

	// Cleanup removes the manifest-tracked set (and manifest), not the user file.
	require.NoError(t, handle.Cleanup())
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", "review.md"))
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", "simple.md"))
	assert.NoFileExists(t, filepath.Join(deliverDir, ".claude", "commands", ".ctxloom-manifest"))
	assert.FileExists(t, userCmd, "user-authored command must survive cleanup")
}

// TestFileTemplateDelivery_DeliverSettings verifies the settings surface (hooks +
// statusline) is written into the injected Placement identically to
// writeSettingsFile, and that Cleanup reverts ctxloom hooks + managed statusline
// while preserving user-authored hooks. It also confirms DeliverSettings does
// not write .mcp.json (that is DeliverMCP's surface).
func TestFileTemplateDelivery_DeliverSettings(t *testing.T) {
	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}},
		},
	}

	deliverDir := t.TempDir()
	d := newFileTemplateDelivery(fakePlacement{dir: deliverDir}, nil)
	handle, err := d.DeliverSettings(hooks, true)
	require.NoError(t, err)

	// Control: the existing settings writer (manageStatusline true == default
	// statusLineDisabled false).
	controlDir := t.TempDir()
	require.NoError(t, (&ClaudeCodeHookWriter{}).writeSettingsFile(hooks, controlDir))

	got, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "settings.json"))
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join(controlDir, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "DeliverSettings must write the same settings.json as writeSettingsFile")

	// The settings surface must not create .mcp.json.
	assert.NoFileExists(t, filepath.Join(deliverDir, ".mcp.json"), "DeliverSettings must not write the MCP surface")

	// Cleanup removes ctxloom hooks and the managed statusline.
	require.NoError(t, handle.Cleanup())
	data, err := os.ReadFile(filepath.Join(deliverDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.NotContains(t, settings, "hooks", "ctxloom hooks must be reverted")
	assert.NotContains(t, settings, "statusLine", "managed statusline must be reverted")
}

// TestFileTemplateDelivery_DeliverSettings_PreservesUserHooks verifies a
// pre-existing user hook survives delivery and cleanup while the ctxloom hook is
// added and then reverted around it.
func TestFileTemplateDelivery_DeliverSettings_PreservesUserHooks(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	existing := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks":   []any{map[string]any{"type": "command", "command": "./user-hook.sh"}},
				},
			},
		},
		"otherSetting": "preserved",
	}
	data, err := json.Marshal(existing)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0o644))

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "ctxloom hook inject-context"}}},
	}
	d := newFileTemplateDelivery(fakePlacement{dir: dir}, nil)
	handle, err := d.DeliverSettings(hooks, true)
	require.NoError(t, err)

	require.NoError(t, handle.Cleanup())

	out, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(out, &settings))

	assert.Equal(t, "preserved", settings["otherSetting"], "unrelated settings must survive")
	hookMap, ok := settings["hooks"].(map[string]any)
	require.True(t, ok, "user hook must survive cleanup")
	require.Contains(t, hookMap, "PreToolUse")
	pre := hookMap["PreToolUse"].([]any)
	require.Len(t, pre, 1)
	m := pre[0].(map[string]any)
	inner := m["hooks"].([]any)
	require.Len(t, inner, 1)
	assert.Equal(t, "./user-hook.sh", inner[0].(map[string]any)["command"])
}
