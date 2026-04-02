// Hooks tests verify that ctxloom correctly manages hooks and MCP servers in
// backend configuration files. This is critical for the context injection
// system - hooks enable ctxloom to inject context at session start, and MCP
// servers expose ctxloom's tools to AI assistants. Tests ensure user-defined
// settings are preserved while ctxloom-managed ones are updated.
package backends

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Hash Computation Tests
// =============================================================================
// Hash-based identification enables ctxloom to track which hooks it manages vs
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

	// Create existing settings with user hooks (no _ctxloom field)
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
							// No _ctxloom field - user-defined
						},
					},
				},
			},
		},
		"otherSetting": "preserved",
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write ctxloom hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{
				{Command: "./ctxloom-hook.sh", Matcher: "Bash"},
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

	// Both user hook and ctxloom hook should exist
	hooks := settings["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Should have 2 matchers (one for user, one for ctxloom) or combined
	totalHooks := 0
	for _, matcher := range preToolUse {
		m := matcher.(map[string]interface{})
		hooksList := m["hooks"].([]interface{})
		totalHooks += len(hooksList)
	}

	if totalHooks < 2 {
		t.Errorf("expected at least 2 hooks (user + ctxloom), got %d", totalHooks)
	}
}

func TestClaudeCodeHookWriter_RemovesOldScmHooks(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with ctxloom hooks (identified by command pattern).
	// Note: We no longer use _ctxloom marker field since Claude Code uses strict
	// schema validation. Hooks are identified by command containing "ctxloom" AND "inject-context".
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
							"command": "\"/old/path/to/ctxloom\" hook inject-context --project \"/some/path\" oldhash123",
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write new ctxloom hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{
				{Command: "./new-ctxloom-hook.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteHooks(cfg, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read back and verify old ctxloom hook is gone
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.json")
	data, _ = os.ReadFile(settingsPath)

	var settings map[string]interface{}
	_ = json.Unmarshal(data, &settings)

	hooks := settings["hooks"].(map[string]interface{})
	preToolUse := hooks["PreToolUse"].([]interface{})

	// Should only have the new ctxloom hook with Edit matcher
	for _, matcher := range preToolUse {
		m := matcher.(map[string]interface{})
		if m["matcher"] == "Bash" {
			hooksList := m["hooks"].([]interface{})
			for _, h := range hooksList {
				hook := h.(map[string]interface{})
				cmd := hook["command"].(string)
				if strings.Contains(cmd, "oldhash123") {
					t.Error("old ctxloom hook should have been removed")
				}
			}
		}
	}
}

// TestClaudeCodeHookWriter_RemovesHooksWithoutMarkerByCommand tests that hooks
// without the _ctxloom field are still removed if they match the inject-context pattern.
// This is a fallback for hooks written by older versions.
func TestClaudeCodeHookWriter_RemovesHooksWithoutMarkerByCommand(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing settings with inject-context hooks that DON'T have _ctxloom field
	// This simulates hooks from an older version or corrupted state
	claudeDir := filepath.Join(tmpDir, ".claude")
	_ = os.MkdirAll(claudeDir, 0755)

	existingSettings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"SessionStart": []interface{}{
				map[string]interface{}{
					"hooks": []interface{}{
						// Old inject-context hook WITHOUT _ctxloom marker
						map[string]interface{}{
							"type":    "command",
							"command": "\"/path/to/ctxloom\" hook inject-context --project \"/some/path\" abc123",
							"timeout": 60,
							// Note: NO "_ctxloom" field - this is the bug case
						},
						// Duplicate
						map[string]interface{}{
							"type":    "command",
							"command": "\"/path/to/ctxloom\" hook inject-context --project \"/some/path\" abc123",
							"timeout": 60,
						},
						// User's own hook (should be preserved)
						map[string]interface{}{
							"type":    "command",
							"command": "echo 'user hook'",
							"timeout": 30,
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(existingSettings)
	_ = os.WriteFile(filepath.Join(claudeDir, "settings.json"), data, 0644)

	// Write new ctxloom hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{
				{Command: "\"/new/ctxloom\" hook inject-context --project \"/new/path\" newhash", Timeout: 60},
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

	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})

	// Count the hooks
	totalHooks := 0
	userHooks := 0
	ctxloomHooks := 0

	for _, matcher := range sessionStart {
		m := matcher.(map[string]interface{})
		hooksList := m["hooks"].([]interface{})
		for _, h := range hooksList {
			hook := h.(map[string]interface{})
			cmd := hook["command"].(string)
			totalHooks++
			if cmd == "echo 'user hook'" {
				userHooks++
			}
			if strings.Contains(cmd, "inject-context") {
				ctxloomHooks++
			}
		}
	}

	// Should have exactly 1 user hook preserved
	if userHooks != 1 {
		t.Errorf("expected 1 user hook, got %d", userHooks)
	}

	// Should have exactly 1 ctxloom hook (the new one, old duplicates removed)
	if ctxloomHooks != 1 {
		t.Errorf("expected 1 ctxloom hook (new), got %d - old hooks may not have been removed", ctxloomHooks)
	}

	// Total should be 2 (1 user + 1 new ctxloom)
	if totalHooks != 2 {
		t.Errorf("expected 2 total hooks, got %d", totalHooks)
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

func TestGetSettingsWriter_AllBackends(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		expected bool
	}{
		{"claude-code", "claude-code", true},
		{"gemini", "gemini", true},
		{"codex", "codex", false},       // No settings support
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

	ctxloomServer, ok := mcpServers["ctxloom"].(map[string]interface{})
	if !ok {
		t.Fatal("expected 'ctxloom' MCP server")
	}

	if _, ok := ctxloomServer["_ctxloom"]; !ok {
		t.Error("ctxloom MCP server should have _ctxloom marker")
	}

	if ctxloomServer["command"] == "" {
		t.Error("ctxloom MCP server should have command")
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
				// No _ctxloom field - user-defined
			},
			"another-server": map[string]interface{}{
				"command": "python",
				"args":    []string{"-m", "mcp_server"},
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks with ctxloom config
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

	// ctxloom server should be added
	if _, ok := mcpServers["ctxloom"]; !ok {
		t.Error("ctxloom MCP server should be added")
	}

	// Verify total count
	if len(mcpServers) != 3 {
		t.Errorf("expected 3 MCP servers (2 user + 1 ctxloom), got %d", len(mcpServers))
	}
}

func TestClaudeCodeHookWriter_UpdatesSCMMCPServer(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Create existing .mcp.json with old ctxloom MCP server
	existingMCP := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"ctxloom": map[string]interface{}{
				"command": "/old/path/to/ctxloom mcp",
				"_ctxloom":    "old-marker",
			},
			"user-server": map[string]interface{}{
				"command": "/usr/bin/user-mcp",
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks - should update ctxloom server
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

	// ctxloom server should be updated (not duplicate)
	ctxloomServer := mcpServers["ctxloom"].(map[string]interface{})
	if ctxloomServer["command"] == "/old/path/to/ctxloom mcp" {
		t.Error("ctxloom server command should be updated")
	}
	if ctxloomServer["_ctxloom"] == "old-marker" {
		t.Error("ctxloom server marker should be updated")
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
	err := WriteSettings("codex", nil, nil, nil, "/project")
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

// =============================================================================
// Schema Resilience Tests
// =============================================================================
// These tests verify that ctxloom gracefully handles malformed or incompatible
// settings.json files, as Claude Code's schema is undocumented and may change.

func TestClaudeCodeHookWriter_ResilienceToMalformedJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	// Create malformed settings.json
	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte("{ invalid json }"), 0644))

	// WriteHooks should NOT fail - it should warn and continue
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err, "should not fail on malformed existing settings.json")

	// Verify hooks were still written
	data, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)

	var settings map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &settings))
	assert.Contains(t, settings, "hooks", "should have hooks after writing")
}

func TestClaudeCodeHookWriter_CreatesBackupBeforeModifying(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	// Create existing valid settings.json
	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	originalContent := `{"existingKey": "originalValue"}`
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(originalContent), 0644))

	// Write hooks
	cfg := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			SessionStart: []config.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err)

	// Verify backup was created
	backupPath := settingsPath + ".ctxloom.bak"
	exists, err := afero.Exists(fs, backupPath)
	require.NoError(t, err)
	assert.True(t, exists, "backup file should be created")

	// Verify backup contains original content
	backupData, err := afero.ReadFile(fs, backupPath)
	require.NoError(t, err)
	assert.Equal(t, originalContent, string(backupData), "backup should contain original content")
}

func TestClaudeCodeHookWriter_MCPConfigResilience(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	// Create malformed .mcp.json
	mcpPath := "/project/.mcp.json"
	require.NoError(t, afero.WriteFile(fs, mcpPath, []byte("not valid json"), 0644))

	// WriteHooks should NOT fail - it should warn and continue
	cfg := &config.HooksConfig{}
	err := writer.WriteHooks(cfg, "/project")
	require.NoError(t, err, "should not fail on malformed .mcp.json")

	// Verify MCP config was still written
	data, err := afero.ReadFile(fs, mcpPath)
	require.NoError(t, err)

	var mcpConfig map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &mcpConfig))
	assert.Contains(t, mcpConfig, "mcpServers", "should have mcpServers after writing")
}

// =============================================================================
// mergeHooksConfig Tests
// =============================================================================
// Tests for merging hooks from multiple profiles/sources.

func TestMergeHooksConfig_NilInputs(t *testing.T) {
	t.Run("nil dest does nothing", func(t *testing.T) {
		src := &config.HooksConfig{
			Unified: config.UnifiedHooks{
				PreTool: []config.Hook{{Command: "test"}},
			},
		}
		// Should not panic
		mergeHooksConfig(nil, src)
	})

	t.Run("nil src does nothing", func(t *testing.T) {
		dest := &config.HooksConfig{}
		mergeHooksConfig(dest, nil)
		assert.Empty(t, dest.Unified.PreTool)
	})

	t.Run("both nil does nothing", func(t *testing.T) {
		mergeHooksConfig(nil, nil)
	})
}

func TestMergeHooksConfig_UnifiedHooks(t *testing.T) {
	dest := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool: []config.Hook{{Command: "existing-pre"}},
		},
	}
	src := &config.HooksConfig{
		Unified: config.UnifiedHooks{
			PreTool:      []config.Hook{{Command: "new-pre"}},
			PostTool:     []config.Hook{{Command: "new-post"}},
			SessionStart: []config.Hook{{Command: "session-start"}},
			SessionEnd:   []config.Hook{{Command: "session-end"}},
			PreShell:     []config.Hook{{Command: "pre-shell"}},
			PostFileEdit: []config.Hook{{Command: "post-edit"}},
		},
	}

	mergeHooksConfig(dest, src)

	assert.Len(t, dest.Unified.PreTool, 2)
	assert.Equal(t, "existing-pre", dest.Unified.PreTool[0].Command)
	assert.Equal(t, "new-pre", dest.Unified.PreTool[1].Command)
	assert.Len(t, dest.Unified.PostTool, 1)
	assert.Len(t, dest.Unified.SessionStart, 1)
	assert.Len(t, dest.Unified.SessionEnd, 1)
	assert.Len(t, dest.Unified.PreShell, 1)
	assert.Len(t, dest.Unified.PostFileEdit, 1)
}

func TestMergeHooksConfig_PluginSpecificHooks(t *testing.T) {
	t.Run("creates plugin map if nil", func(t *testing.T) {
		dest := &config.HooksConfig{}
		src := &config.HooksConfig{
			Plugins: map[string]config.BackendHooks{
				"claude-code": {
					"PreTool": []config.Hook{{Command: "claude-hook"}},
				},
			},
		}

		mergeHooksConfig(dest, src)

		assert.NotNil(t, dest.Plugins)
		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 1)
	})

	t.Run("merges into existing plugins", func(t *testing.T) {
		dest := &config.HooksConfig{
			Plugins: map[string]config.BackendHooks{
				"claude-code": {
					"PreTool": []config.Hook{{Command: "existing"}},
				},
			},
		}
		src := &config.HooksConfig{
			Plugins: map[string]config.BackendHooks{
				"claude-code": {
					"PreTool":  []config.Hook{{Command: "new"}},
					"PostTool": []config.Hook{{Command: "post"}},
				},
				"gemini": {
					"PreTool": []config.Hook{{Command: "gemini-hook"}},
				},
			},
		}

		mergeHooksConfig(dest, src)

		assert.Len(t, dest.Plugins["claude-code"]["PreTool"], 2)
		assert.Len(t, dest.Plugins["claude-code"]["PostTool"], 1)
		assert.Len(t, dest.Plugins["gemini"]["PreTool"], 1)
	})
}

// =============================================================================
// MergeMCPConfig Tests
// =============================================================================
// Tests for merging MCP server configurations from multiple profiles/sources.

func TestMergeMCPConfig_NilInputs(t *testing.T) {
	t.Run("nil dest does nothing", func(t *testing.T) {
		src := &config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"test": {Command: "test-cmd"},
			},
		}
		config.MergeMCPConfig(nil, src)
	})

	t.Run("nil src does nothing", func(t *testing.T) {
		dest := &config.MCPConfig{}
		config.MergeMCPConfig(dest, nil)
		assert.Nil(t, dest.Servers)
	})

	t.Run("both nil does nothing", func(t *testing.T) {
		config.MergeMCPConfig(nil, nil)
	})
}

func TestMergeMCPConfig_AutoRegisterCtxloom(t *testing.T) {
	t.Run("src overrides dest", func(t *testing.T) {
		trueVal := true
		falseVal := false
		dest := &config.MCPConfig{AutoRegisterCtxloom: &trueVal}
		src := &config.MCPConfig{AutoRegisterCtxloom: &falseVal}

		config.MergeMCPConfig(dest, src)

		assert.False(t, *dest.AutoRegisterCtxloom)
	})

	t.Run("nil src preserves dest", func(t *testing.T) {
		trueVal := true
		dest := &config.MCPConfig{AutoRegisterCtxloom: &trueVal}
		src := &config.MCPConfig{}

		config.MergeMCPConfig(dest, src)

		assert.True(t, *dest.AutoRegisterCtxloom)
	})
}

func TestMergeMCPConfig_UnifiedServers(t *testing.T) {
	t.Run("creates servers map if nil", func(t *testing.T) {
		dest := &config.MCPConfig{}
		src := &config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"test-server": {Command: "test-cmd", Args: []string{"arg1"}},
			},
		}

		config.MergeMCPConfig(dest, src)

		assert.NotNil(t, dest.Servers)
		assert.Equal(t, "test-cmd", dest.Servers["test-server"].Command)
	})

	t.Run("src overrides dest for same name", func(t *testing.T) {
		dest := &config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"server": {Command: "old-cmd"},
			},
		}
		src := &config.MCPConfig{
			Servers: map[string]config.MCPServer{
				"server": {Command: "new-cmd"},
			},
		}

		config.MergeMCPConfig(dest, src)

		assert.Equal(t, "new-cmd", dest.Servers["server"].Command)
	})
}

func TestMergeMCPConfig_PluginSpecificServers(t *testing.T) {
	t.Run("creates plugin map if nil", func(t *testing.T) {
		dest := &config.MCPConfig{}
		src := &config.MCPConfig{
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"my-server": {Command: "my-cmd"},
				},
			},
		}

		config.MergeMCPConfig(dest, src)

		assert.NotNil(t, dest.Plugins)
		assert.Equal(t, "my-cmd", dest.Plugins["claude-code"]["my-server"].Command)
	})

	t.Run("merges multiple backends", func(t *testing.T) {
		dest := &config.MCPConfig{
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"existing": {Command: "existing-cmd"},
				},
			},
		}
		src := &config.MCPConfig{
			Plugins: map[string]map[string]config.MCPServer{
				"claude-code": {
					"new": {Command: "new-cmd"},
				},
				"gemini": {
					"gemini-server": {Command: "gemini-cmd"},
				},
			},
		}

		config.MergeMCPConfig(dest, src)

		assert.Equal(t, "existing-cmd", dest.Plugins["claude-code"]["existing"].Command)
		assert.Equal(t, "new-cmd", dest.Plugins["claude-code"]["new"].Command)
		assert.Equal(t, "gemini-cmd", dest.Plugins["gemini"]["gemini-server"].Command)
	})
}

// =============================================================================
// Helper Function Tests
// =============================================================================
// Tests for shared helper functions that reduce code duplication.

func TestAtomicWriteFile(t *testing.T) {
	t.Run("writes new file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		data := []byte(`{"key": "value"}`)

		err := atomicWriteFile(fs, path, data, "test file")
		require.NoError(t, err)

		// Verify file contents
		contents, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, data, contents)
	})

	t.Run("creates backup of existing file", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		original := []byte(`{"original": true}`)
		updated := []byte(`{"updated": true}`)

		// Create original file
		require.NoError(t, afero.WriteFile(fs, path, original, 0644))

		// Write new content
		err := atomicWriteFile(fs, path, updated, "test file")
		require.NoError(t, err)

		// Verify backup exists with original content
		backupPath := path + ".ctxloom.bak"
		backup, err := afero.ReadFile(fs, backupPath)
		require.NoError(t, err)
		assert.Equal(t, original, backup)

		// Verify file has new content
		contents, err := afero.ReadFile(fs, path)
		require.NoError(t, err)
		assert.Equal(t, updated, contents)
	})

	t.Run("cleans up temp file on success", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		path := "/test/file.json"
		data := []byte(`{"key": "value"}`)

		err := atomicWriteFile(fs, path, data, "test file")
		require.NoError(t, err)

		// Temp file should not exist
		tmpPath := path + ".ctxloom.tmp"
		exists, _ := afero.Exists(fs, tmpPath)
		assert.False(t, exists, "temp file should be cleaned up")
	})
}

func TestWarn(t *testing.T) {
	// Capture stderr
	// Note: warn() outputs to os.Stderr, which is hard to capture in tests.
	// This test just verifies the function doesn't panic.
	warn("test warning: %s", "message")
}

func TestGetFS(t *testing.T) {
	t.Run("returns provided fs", func(t *testing.T) {
		memFs := afero.NewMemMapFs()
		result := getFS(memFs)
		assert.Equal(t, memFs, result)
	})

	t.Run("returns OsFs when nil", func(t *testing.T) {
		result := getFS(nil)
		assert.NotNil(t, result)
		// Can't directly compare to OsFs, but it shouldn't be nil
	})
}

