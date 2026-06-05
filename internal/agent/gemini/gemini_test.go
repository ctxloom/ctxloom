package gemini

import (
	"encoding/json"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiHookWriter_SettingsPath(t *testing.T) {
	writer := &GeminiHookWriter{}

	path := writer.SettingsPath("/project")
	assert.Equal(t, "/project/.gemini/settings.json", path)
}
func TestGeminiHookWriter_HooksPath(t *testing.T) {
	writer := &GeminiHookWriter{}

	path := writer.HooksPath("/project")
	assert.Equal(t, "/project/.gemini/settings.json", path)
}
func TestGeminiHookWriter_WriteHooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &GeminiHookWriter{FS: fs}

	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{
				{Command: "./pre-tool.sh", Matcher: "Bash"},
			},
			PostTool: []config.Hook{
				{Command: "./post-tool.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err)

	// Verify file was created
	settingsPath := "/project/.gemini/settings.json"
	exists, err := afero.Exists(fs, settingsPath)
	require.NoError(t, err)
	assert.True(t, exists, "settings.json should be created")

	data, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)

	var settings map[string]interface{}
	err = json.Unmarshal(data, &settings)
	require.NoError(t, err)

	// Gemini settings should have hooks key
	_, hasHooks := settings["hooks"]
	assert.True(t, hasHooks, "settings should contain hooks")
}
func TestGeminiHookWriter_PreservesUserSettings(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &GeminiHookWriter{FS: fs}

	// Create existing settings with user config
	_ = fs.MkdirAll("/project/.gemini", 0755)
	existingSettings := map[string]interface{}{
		"userSetting": "preserved",
		"model":       "gemini-pro",
	}
	data, _ := json.Marshal(existingSettings)
	_ = afero.WriteFile(fs, "/project/.gemini/settings.json", data, 0644)

	// Write ctxloom hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{{Command: "./start.sh"}},
		},
	}

	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err)

	// Read back and verify user settings preserved
	data, _ = afero.ReadFile(fs, "/project/.gemini/settings.json")
	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	assert.Equal(t, "preserved", settings["userSetting"])
	assert.Equal(t, "gemini-pro", settings["model"])
}
func TestGeminiHookWriter_WriteSettings_WithMCP(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &GeminiHookWriter{FS: fs}

	hooks := &config.HooksConfig{}
	mcp := &config.MCPConfig{
		Servers: map[string]config.MCPServer{
			"custom-server": {
				Command: "custom-mcp",
				Args:    []string{"--port", "3000"},
			},
		},
	}

	err := writer.WriteSettings(hooks, mcp, nil, "/project")
	require.NoError(t, err)

	// Verify MCP servers written
	data, _ := afero.ReadFile(fs, "/project/.gemini/settings.json")
	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	// Gemini should have mcpServers in settings
	mcpServers, ok := settings["mcpServers"].(map[string]interface{})
	assert.True(t, ok, "should have mcpServers in settings")

	// ctxloom server should be added
	_, hasCtxloom := mcpServers["ctxloom"]
	assert.True(t, hasCtxloom, "should have ctxloom MCP server")

	// Custom server should be added
	_, hasCustom := mcpServers["custom-server"]
	assert.True(t, hasCustom, "should have custom-server")
}
func TestGeminiHookWriter_NestedSchema(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &GeminiHookWriter{FS: fs}

	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{{Command: "ctxloom hook session-bind", Timeout: 60}},
		},
	}
	require.NoError(t, writer.WriteHooks(cfg, "/project"))

	data, err := afero.ReadFile(fs, "/project/.gemini/settings.json")
	require.NoError(t, err)

	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Name    string `json:"name"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &settings))

	groups := settings.Hooks["SessionStart"]
	require.Len(t, groups, 1)
	require.Len(t, groups[0].Hooks, 1)
	entry := groups[0].Hooks[0]
	assert.Equal(t, "command", entry.Type, "Gemini requires type:command")
	assert.Equal(t, "ctxloom hook session-bind", entry.Command)
	assert.Equal(t, "ctxloom-managed", entry.Name, "durable ctxloom marker")
	assert.Equal(t, 60000, entry.Timeout, "timeout must be milliseconds (60s → 60000ms)")
}
func TestGeminiHookWriter_RemovesManagedHooks(t *testing.T) {
	fs := afero.NewMemMapFs()
	_ = fs.MkdirAll("/project/.gemini", 0755)
	// Existing file: one user hook + one stale ctxloom-managed hook.
	existing := `{"hooks":{"SessionStart":[
		{"matcher":"","hooks":[{"type":"command","command":"user-hook.sh"}]},
		{"matcher":"","hooks":[{"type":"command","command":"ctxloom hook inject-context old","name":"ctxloom-managed"}]}
	]}}`
	require.NoError(t, afero.WriteFile(fs, "/project/.gemini/settings.json", []byte(existing), 0644))

	writer := &GeminiHookWriter{FS: fs}
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{SessionStart: []config.Hook{{Command: "ctxloom hook session-bind"}}},
	}
	require.NoError(t, writer.WriteHooks(cfg, "/project"))

	data, _ := afero.ReadFile(fs, "/project/.gemini/settings.json")
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(data, &settings))

	var commands []string
	for _, g := range settings.Hooks["SessionStart"] {
		for _, e := range g.Hooks {
			commands = append(commands, e.Command)
		}
	}
	assert.Contains(t, commands, "user-hook.sh", "user hook preserved")
	assert.Contains(t, commands, "ctxloom hook session-bind", "fresh ctxloom hook present")
	assert.NotContains(t, commands, "ctxloom hook inject-context old", "stale ctxloom hook removed")
}
func TestGeminiHookWriter_WithFS(t *testing.T) {
	// Verify that FS injection works for isolated testing
	fs := afero.NewMemMapFs()
	writer := &GeminiHookWriter{FS: fs}

	cfg := &config.HooksConfig{}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err)

	// Should create .gemini directory
	exists, _ := afero.DirExists(fs, "/project/.gemini")
	assert.True(t, exists)
}
