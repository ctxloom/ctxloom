// Hooks tests verify that SCM correctly manages hooks and MCP servers in
// backend configuration files. This is critical for the context injection
// system - hooks enable SCM to inject context at session start, and MCP
// servers expose SCM's tools to AI assistants. Tests ensure user-defined
// settings are preserved while SCM-managed ones are updated.
package backends

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Hash Computation Tests
// =============================================================================
// Hash-based identification enables SCM to track which hooks it manages vs
// user-defined hooks, allowing clean updates without losing user customization.

func TestComputeHookHash(t *testing.T) {
	h1 := config.Hook{Command: "./test.sh", Matcher: "Bash"}
	h2 := config.Hook{Command: "./test.sh", Matcher: "Bash"}
	h3 := config.Hook{Command: "./other.sh", Matcher: "Bash"}

	hash1 := computeHookHash(h1)
	hash2 := computeHookHash(h2)
	hash3 := computeHookHash(h3)

	if hash1 != hash2 {
		t.Errorf("same hooks should have same hash: %s != %s", hash1, hash2)
	}
	if hash1 == hash3 {
		t.Error("different hooks should have different hashes")
	}
	if len(hash1) != 16 {
		t.Errorf("expected 16 char hash, got %d", len(hash1))
	}
}

// =============================================================================
// Claude Code Hook Writer Tests
// =============================================================================
// Claude Code stores hooks in .claude/settings.json and MCP servers in .mcp.json.
// The split is required for variable expansion (${CLAUDE_PROJECT_DIR}) to work.

func TestClaudeCodeHookWriter_WriteHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

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

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was created
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("failed to read settings.json: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("failed to parse settings.json: %v", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("expected hooks in settings")
	}

	// Check PreToolUse
	preToolUse, ok := hooks["PreToolUse"].([]interface{})
	if !ok || len(preToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse matcher, got %v", hooks["PreToolUse"])
	}

	// Check PostToolUse
	postToolUse, ok := hooks["PostToolUse"].([]interface{})
	if !ok || len(postToolUse) != 1 {
		t.Errorf("expected 1 PostToolUse matcher, got %v", hooks["PostToolUse"])
	}
}

func TestClaudeCodeHookWriter_PreservesUserHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with user hooks (no _scm field)
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)

	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "./user-hook.sh",
							// No _scm field - user-defined
						},
					},
				},
			},
		},
		"otherSetting": "preserved",
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write SCM hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{
				{Command: "./scm-hook.sh", Matcher: "Bash"},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and verify
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ = os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	// otherSetting should be preserved
	if settings["otherSetting"] != "preserved" {
		t.Error("expected otherSetting to be preserved")
	}

	// Both user hook and SCM hook should exist
	hooks := settings["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Should have 2 matchers (one for user, one for SCM) or combined
	totalHooks := 0
	for _, matcher := range preToolUse {
		m := matcher.(map[string]interface{})
		hooksList := m["hooks"].([]interface{})
		totalHooks += len(hooksList)
	}

	if totalHooks < 2 {
		t.Errorf("expected at least 2 hooks (user + SCM), got %d", totalHooks)
	}
}

func TestClaudeCodeHookWriter_RemovesOldScmHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with SCM hooks (_scm field present)
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)

	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []interface{}{
				map[string]interface{}{
					"matcher": "Bash",
					"hooks": []interface{}{
						map[string]interface{}{
							"type":    "command",
							"command": "./old-scm-hook.sh",
							"_scm":    "oldhash123", // SCM-managed
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write new SCM hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{
				{Command: "./new-scm-hook.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and verify old SCM hook is gone
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ = os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Should only have the new SCM hook with Edit matcher
	for _, matcher := range preToolUse {
		m := matcher.(map[string]interface{})
		if m["matcher"] == "Bash" {
			hooksList := m["hooks"].([]interface{})
			for _, h := range hooksList {
				hook := h.(map[string]interface{})
				if hook["command"] == "./old-scm-hook.sh" {
					t.Error("old SCM hook should have been removed")
				}
			}
		}
	}
}

func TestClaudeCodeHookWriter_UnifiedToBackendMapping(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreShell:     []config.Hook{{Command: "./pre-shell.sh"}},
			PostFileEdit: []config.Hook{{Command: "./post-edit.sh"}},
			SessionStart: []config.Hook{{Command: "./start.sh"}},
			SessionEnd:   []config.Hook{{Command: "./end.sh"}},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ := os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})

	// PreShell maps to PreToolUse with Bash matcher
	preToolUse := hooks["PreToolUse"].([]interface{})
	foundBashMatcher := false
	for _, m := range preToolUse {
		matcher := m.(map[string]interface{})
		if matcher["matcher"] == "Bash" {
			foundBashMatcher = true
		}
	}
	if !foundBashMatcher {
		t.Error("PreShell should map to PreToolUse with Bash matcher")
	}

	// PostFileEdit maps to PostToolUse with Edit|Write matcher
	postToolUse := hooks["PostToolUse"].([]interface{})
	foundEditMatcher := false
	for _, m := range postToolUse {
		matcher := m.(map[string]interface{})
		if matcher["matcher"] == "Edit|Write" {
			foundEditMatcher = true
		}
	}
	if !foundEditMatcher {
		t.Error("PostFileEdit should map to PostToolUse with Edit|Write matcher")
	}

	// SessionStart and SessionEnd should be present
	if _, ok := hooks["SessionStart"]; !ok {
		t.Error("expected SessionStart hook")
	}
	if _, ok := hooks["SessionEnd"]; !ok {
		t.Error("expected SessionEnd hook")
	}
}

func TestClaudeCodeHookWriter_BackendPassthrough(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &config.HooksConfig{
		Plugins: map[string]config.BackendHooks{
			"claude-code": {
				"Notification": []config.Hook{
					{Command: "./notify.sh", Type: "command"},
				},
				"PreCompact": []config.Hook{
					{Command: "./compact.sh"},
				},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ := os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})

	if _, ok := hooks["Notification"]; !ok {
		t.Error("expected Notification hook from passthrough")
	}
	if _, ok := hooks["PreCompact"]; !ok {
		t.Error("expected PreCompact hook from passthrough")
	}
}

// =============================================================================
// Settings Writer Factory Tests
// =============================================================================
// Factory enables runtime backend selection based on user config.

func TestGetHookWriter(t *testing.T) {
	if GetHookWriter("claude-code") == nil {
		t.Error("expected hook writer for claude-code")
	}
	if GetHookWriter("unknown-backend") != nil {
		t.Error("expected nil for unknown backend")
	}
}

func TestGetSettingsWriter_AllBackends(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		expected bool
	}{
		{"claude-code", "claude-code", true},
		{"gemini", "gemini", true},
		{"aider", "aider", false},       // No settings support
		{"unknown", "unknown", false},   // Unknown backend
		{"empty", "", false},            // Empty string
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := GetSettingsWriter(tt.backend, nil)
			if tt.expected {
				assert.NotNil(t, writer)
			} else {
				assert.Nil(t, writer)
			}
		})
	}
}

func TestClaudeCodeHookWriter_MCPServerInjection(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Empty config should still add MCP server
	cfg := &config.HooksConfig{}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// MCP servers are now written to .mcp.json (not settings.json)
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("failed to read .mcp.json: %v", err)
	}

	var mcpConfig map[string]interface{}
	if err := json.Unmarshal(data, &mcpConfig); err != nil {
		t.Fatalf("failed to parse .mcp.json: %v", err)
	}

	mcpServers, ok := mcpConfig["mcpServers"].(map[string]interface{})
	if !ok {
		t.Fatal("expected mcpServers in .mcp.json")
	}

	scmServer, ok := mcpServers["scm"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'scm' MCP server")
	}

	if _, ok := scmServer["_scm"]; !ok {
		t.Error("SCM MCP server should have _scm marker")
	}

	if scmServer["command"] == "" {
		t.Error("SCM MCP server should have command")
	}

	// Verify settings.json does NOT contain mcpServers
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var settings map[string]interface{}
		_ = json.Unmarshal(data, &settings)
		if _, ok := settings["mcpServers"]; ok {
			t.Error("settings.json should NOT contain mcpServers (they belong in .mcp.json)")
		}
	}
}

func TestClaudeCodeHookWriter_PreservesUserMCPServers(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing .mcp.json with user-defined MCP servers
	existingMCP := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"my-custom-server": map[string]interface{}{
				"command": "/usr/local/bin/my-mcp-server",
				"args":    []string{"--port", "3000"},
				// No _scm field - user-defined
			},
			"another-server": map[string]interface{}{
				"command": "python",
				"args":    []string{"-m", "mcp_server"},
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks with SCM config
	cfg := &config.HooksConfig{}
	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read updated .mcp.json
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, _ = os.ReadFile(mcpPath)

	var mcpConfig map[string]interface{}
	_ = json.Unmarshal(data, &mcpConfig)

	mcpServers := mcpConfig["mcpServers"].(map[string]interface{})

	// User servers should be preserved
	if _, ok := mcpServers["my-custom-server"]; !ok {
		t.Error("user-defined 'my-custom-server' should be preserved")
	}
	if _, ok := mcpServers["another-server"]; !ok {
		t.Error("user-defined 'another-server' should be preserved")
	}

	// SCM server should be added
	if _, ok := mcpServers["scm"]; !ok {
		t.Error("SCM MCP server should be added")
	}

	// Verify total count
	if len(mcpServers) != 3 {
		t.Errorf("expected 3 MCP servers (2 user + 1 scm), got %d", len(mcpServers))
	}
}

func TestClaudeCodeHookWriter_UpdatesSCMMCPServer(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing .mcp.json with old SCM MCP server
	existingMCP := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"scm": map[string]interface{}{
				"command": "/old/path/to/scm mcp",
				"_scm":    "old-marker",
			},
			"user-server": map[string]interface{}{
				"command": "/usr/bin/user-mcp",
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks - should update SCM server
	cfg := &config.HooksConfig{}
	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read updated .mcp.json
	mcpPath := filepath.Join(tmpDir, ".mcp.json")
	data, _ = os.ReadFile(mcpPath)

	var mcpConfig map[string]interface{}
	_ = json.Unmarshal(data, &mcpConfig)

	mcpServers := mcpConfig["mcpServers"].(map[string]interface{})

	// User server should be preserved
	if _, ok := mcpServers["user-server"]; !ok {
		t.Error("user-defined server should be preserved")
	}

	// SCM server should be updated (not duplicate)
	scmServer := mcpServers["scm"].(map[string]interface{})
	if scmServer["command"] == "/old/path/to/scm mcp" {
		t.Error("SCM server command should be updated")
	}
	if scmServer["_scm"] == "old-marker" {
		t.Error("SCM server marker should be updated")
	}

	// Should still have exactly 2 servers
	if len(mcpServers) != 2 {
		t.Errorf("expected 2 MCP servers, got %d", len(mcpServers))
	}
}

// =============================================================================
// Gemini Hook Writer Tests
// =============================================================================
// Gemini stores settings in .gemini/settings.json with a different format than
// Claude Code. Tests ensure hooks are written in Gemini's expected structure.

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

	// Write SCM hooks
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

	// SCM server should be added
	_, hasScm := mcpServers["scm"]
	assert.True(t, hasScm, "should have scm MCP server")

	// Custom server should be added
	_, hasCustom := mcpServers["custom-server"]
	assert.True(t, hasCustom, "should have custom-server")
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

// =============================================================================
// WriteSettings Function Tests
// =============================================================================
// Top-level WriteSettings dispatches to appropriate backend writer.

func TestWriteSettings_UnsupportedBackend(t *testing.T) {
	// Unsupported backends should silently succeed (no-op)
	err := WriteSettings("aider", nil, nil, nil, "/project")
	assert.NoError(t, err)
}

func TestWriteSettings_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()

	hooks := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{{Command: "./test.sh"}},
		},
	}

	err := WriteSettings("claude-code", hooks, nil, nil, "/project", WithSettingsFS(fs))
	require.NoError(t, err)

	// Verify settings were written
	exists, _ := afero.Exists(fs, "/project/.claude/settings.json")
	assert.True(t, exists)
}

