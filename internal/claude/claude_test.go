package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClaudeCodeHookWriter_WriteSettings(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{
				{Command: "./pre-tool.sh", Matcher: "Bash"},
			},
			PostTool: []wire.Hook{
				{Command: "./post-tool.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{
				{Command: "./ctxloom-hook.sh", Matcher: "Bash"},
			},
		},
	}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{
				{Command: "./new-ctxloom-hook.sh", Matcher: "Edit"},
			},
		},
	}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{
				{Command: "\"/new/ctxloom\" hook inject-context --project \"/new/path\" newhash", Timeout: 60},
			},
		},
	}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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
func TestClaudeCodeHookWriter_DedupsBundleShippedHooks(t *testing.T) {
	tmpDir := t.TempDir()

	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PostFileEdit: []wire.Hook{
				{Command: "ctxloom hook stamp-plan", Type: "command"},
			},
		},
	}

	// Write hooks three times. Without the fix, each apply would append
	// a duplicate of the previous run's hooks.
	for range 3 {
		if err := writer.WriteSettings(cfg, nil, nil, tmpDir); err != nil {
			t.Fatalf("WriteSettings: %v", err)
		}
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The stamp-plan hook (PostFileEdit → PostToolUse Edit|Write) must
	// appear exactly once across the whole PostToolUse tree, not once
	// per apply.
	hooks := settings["hooks"].(map[string]any)
	postToolUse := hooks["PostToolUse"].([]any)
	stampCount := 0
	for _, matcher := range postToolUse {
		m := matcher.(map[string]any)
		hooksList := m["hooks"].([]any)
		for _, h := range hooksList {
			cmd := h.(map[string]any)["command"].(string)
			if strings.Contains(cmd, "hook stamp-plan") {
				stampCount++
			}
		}
	}
	if stampCount != 1 {
		t.Errorf("expected exactly 1 `hook stamp-plan` hook after 3 applies, got %d", stampCount)
	}
}

// Companion-binary hooks (executable ≠ ctxloom, no durable marker possible
// under Claude Code's strict settings schema) dedupe by exact command on
// re-apply; a user variant of the same binary with different args survives.
func TestClaudeCodeHookWriter_CompanionHookIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	claudeDir := filepath.Join(tmpDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	// User's own ltk registration (different args) predates ctxloom's.
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"ltk evaluate --config .ltk/config.yaml"}]}]}}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o644))

	writer := &ClaudeCodeHookWriter{}
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreTool: []wire.Hook{{Command: "ltk evaluate", Matcher: "Bash", SCM: "bundle:builtin:ltk"}},
		},
	}
	for range 3 {
		require.NoError(t, writer.WriteSettings(cfg, nil, nil, tmpDir))
	}

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))

	exact, variant := 0, 0
	for _, matcher := range settings["hooks"].(map[string]any)["PreToolUse"].([]any) {
		for _, h := range matcher.(map[string]any)["hooks"].([]any) {
			switch h.(map[string]any)["command"].(string) {
			case "ltk evaluate":
				exact++
			case "ltk evaluate --config .ltk/config.yaml":
				variant++
			}
		}
	}
	assert.Equal(t, 1, exact, "companion hook must not duplicate across re-applies")
	assert.Equal(t, 1, variant, "user's own variant of the same binary must survive")
}
func TestClaudeCodeHookWriter_UnifiedToBackendMapping(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			PreShell:     []wire.Hook{{Command: "./pre-shell.sh"}},
			PostFileEdit: []wire.Hook{{Command: "./post-edit.sh"}},
			SessionStart: []wire.Hook{{Command: "./start.sh"}},
			SessionEnd:   []wire.Hook{{Command: "./end.sh"}},
		},
	}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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

	cfg := &wire.HooksConfig{
		Plugins: map[string]wire.BackendHooks{
			"claude-code": {
				"Notification": []wire.Hook{
					{Command: "./notify.sh", Type: "command"},
				},
				"PreCompact": []wire.Hook{
					{Command: "./compact.sh"},
				},
			},
		},
	}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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
func TestClaudeCodeHookWriter_MCPServerInjection(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	// Empty config should still add MCP server
	cfg := &wire.HooksConfig{}

	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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

// TestClaudeCodeHookWriter_MCPCommandOverride pins dire-five's fix at claude's
// own writer (which does NOT ride the shared agent.MCPFileConfig reconciler —
// claude has its own .mcp.json shape): a zero-value writer (mcpCommandOverride
// unset — every cell but an isolated container) emits EXACTLY
// agent.CtxloomCommand()'s host self-exec-absolute path; a writer with the
// override set (the container-cell path, surfacedelivery.go's DeliverMCP)
// emits the override instead.
func TestClaudeCodeHookWriter_MCPCommandOverride(t *testing.T) {
	readCommand := func(t *testing.T, dir string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, ".mcp.json"))
		require.NoError(t, err)
		var cfg map[string]interface{}
		require.NoError(t, json.Unmarshal(data, &cfg))
		servers := cfg["mcpServers"].(map[string]interface{})
		ctxloomServer := servers["ctxloom"].(map[string]interface{})
		return ctxloomServer["command"].(string)
	}

	t.Run("host-unchanged: no override writes CtxloomCommand()", func(t *testing.T) {
		tmpDir := t.TempDir()
		writer := &ClaudeCodeHookWriter{}
		require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, nil, tmpDir))
		assert.Equal(t, agent.CtxloomCommand(), readCommand(t, tmpDir))
	})

	t.Run("container cell: override wins", func(t *testing.T) {
		tmpDir := t.TempDir()
		const containerBin = "/usr/local/bin/ctxloom"
		writer := &ClaudeCodeHookWriter{mcpCommandOverride: containerBin}
		require.NoError(t, writer.WriteSettings(&wire.HooksConfig{}, nil, nil, tmpDir))
		assert.Equal(t, containerBin, readCommand(t, tmpDir))
	})
}

func TestClaudeCodeHookWriter_MCPServerEnvPreserved(t *testing.T) {
	tmpDir := t.TempDir()
	writer := &ClaudeCodeHookWriter{}

	mcp := &wire.MCPConfig{
		Servers: map[string]wire.MCPServer{
			"config-server": {
				Command: "config-cmd",
				Args:    []string{"--flag"},
				Env:     map[string]string{"CONFIG_TOKEN": "abc123"},
			},
		},
		Plugins: map[string]map[string]wire.MCPServer{
			"claude-code": {
				"plugin-server": {
					Command: "plugin-cmd",
					Env:     map[string]string{"PLUGIN_KEY": "xyz"},
				},
			},
		},
	}
	bundleMCP := map[string]wire.MCPServer{
		"bundle-server": {
			Command: "bundle-cmd",
			Env:     map[string]string{"BUNDLE_VAR": "value", "OTHER": "2"},
			SCM:     "ctxloom-bundle:test",
		},
	}

	err := writer.WriteSettings(&wire.HooksConfig{}, mcp, bundleMCP, tmpDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, ".mcp.json"))
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

	wantEnv := map[string]map[string]string{
		"config-server": {"CONFIG_TOKEN": "abc123"},
		"plugin-server": {"PLUGIN_KEY": "xyz"},
		"bundle-server": {"BUNDLE_VAR": "value", "OTHER": "2"},
	}
	for name, want := range wantEnv {
		server, ok := mcpServers[name].(map[string]interface{})
		if !ok {
			t.Fatalf("expected %q MCP server in .mcp.json", name)
		}
		env, ok := server["env"].(map[string]interface{})
		if !ok {
			t.Fatalf("%q: env vars were dropped from .mcp.json", name)
		}
		for k, v := range want {
			if env[k] != v {
				t.Errorf("%q: env[%q] = %v, want %q", name, k, env[k], v)
			}
		}
	}

	// The auto-registered ctxloom server has no env and must not gain one.
	ctxloomServer := mcpServers["ctxloom"].(map[string]interface{})
	if _, ok := ctxloomServer["env"]; ok {
		t.Error("ctxloom auto-registered server should not have an env key")
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
	cfg := &wire.HooksConfig{}
	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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
				"command":  "/old/path/to/ctxloom mcp",
				"_ctxloom": "old-marker",
			},
			"user-server": map[string]interface{}{
				"command": "/usr/bin/user-mcp",
			},
		},
	}

	data, _ := json.MarshalIndent(existingMCP, "", "  ")
	_ = os.WriteFile(filepath.Join(tmpDir, ".mcp.json"), data, 0644)

	// Write hooks - should update ctxloom server
	cfg := &wire.HooksConfig{}
	err := writer.WriteSettings(cfg, nil, nil, tmpDir)
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

// TestClaudeCodeHookWriter_MalformedSettingsJSON_FailsLoudAndBacksUp is a
// regression test for taskloom lone-taste: loadSettings used to swallow a
// top-level unmarshal failure and return an empty-but-valid settings object,
// which WriteSettings then persisted OVER the user's real settings.json —
// destroying their permissions/env. It must now (a) leave the original file
// untouched, (b) back up the corrupt bytes to a sibling .corrupt-<ts> file,
// and (c) return an error so the caller aborts instead of overwriting.
func TestClaudeCodeHookWriter_MalformedSettingsJSON_FailsLoudAndBacksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	corruptContent := "{ invalid json }"
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(corruptContent), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteSettings(cfg, nil, nil, "/project")
	require.Error(t, err, "should refuse to write when existing settings.json fails to parse")

	// Original file must be left exactly as-is, not overwritten with an
	// empty (or hooks-only) settings object.
	data, readErr := afero.ReadFile(fs, settingsPath)
	require.NoError(t, readErr)
	assert.Equal(t, corruptContent, string(data), "original corrupt settings.json must not be overwritten")

	assert.True(t, hasCorruptBackup(t, fs, "/project/.claude", "settings.json", corruptContent),
		"expected a settings.json.corrupt-<timestamp> backup of the original bytes")
}

// TestClaudeCodeHookWriter_MalformedHooksJSON_FailsLoudAndBacksUp covers the
// second half of taskloom lone-taste: the top-level JSON parses fine but the
// "hooks" field itself doesn't unmarshal into the expected shape. This must
// fail the same way as a fully corrupt file, not silently drop the user's
// existing hooks.
func TestClaudeCodeHookWriter_MalformedHooksJSON_FailsLoudAndBacksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	corruptContent := `{"hooks": "not-an-object", "permissions": {"allow": ["Bash"]}}`
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(corruptContent), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteSettings(cfg, nil, nil, "/project")
	require.Error(t, err, "should refuse to write when hooks in settings.json fail to parse")

	data, readErr := afero.ReadFile(fs, settingsPath)
	require.NoError(t, readErr)
	assert.Equal(t, corruptContent, string(data), "original settings.json with unparseable hooks must not be overwritten")

	assert.True(t, hasCorruptBackup(t, fs, "/project/.claude", "settings.json", corruptContent),
		"expected a settings.json.corrupt-<timestamp> backup of the original bytes")
}

// hasCorruptBackup reports whether dir contains a "<name>.corrupt-<ts>" file
// whose contents equal want.
func hasCorruptBackup(t *testing.T, fs afero.Fs, dir, name, want string) bool {
	t.Helper()
	entries, err := afero.ReadDir(fs, dir)
	require.NoError(t, err)
	prefix := name + ".corrupt-"
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			data, err := afero.ReadFile(fs, filepath.Join(dir, e.Name()))
			require.NoError(t, err)
			return string(data) == want
		}
	}
	return false
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
	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "./test.sh"}},
		},
	}
	err := writer.WriteSettings(cfg, nil, nil, "/project")
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

// This test used to assert the OPPOSITE — that a malformed .mcp.json was
// warned about and then overwritten, described as "resilience" (U032-F05).
// What it actually pinned was the deletion of every MCP server the user had:
// loadMCPConfig returned a fresh empty config, writeMCPConfig filled it with
// ctxloom's servers and saved. Resilience is refusing to write, not writing
// anyway; the contract is now the same one loadSettings has had since
// taskloom lone-taste.
func TestClaudeCodeHookWriter_MalformedMCPConfig_IsNotOverwritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	mcpPath := "/project/.mcp.json"
	malformed := "not valid json"
	require.NoError(t, afero.WriteFile(fs, mcpPath, []byte(malformed), 0644))

	cfg := &wire.HooksConfig{}
	err := writer.WriteSettings(cfg, nil, nil, "/project")
	require.Error(t, err, "an unreadable .mcp.json must stop the write, not be replaced by one")

	data, readErr := afero.ReadFile(fs, mcpPath)
	require.NoError(t, readErr)
	assert.Equal(t, malformed, string(data), "the user's .mcp.json must be left exactly as it was")
}

// permissionsPayload reads settings.json and returns its permissions block —
// the test helper every deny-tools payload assertion below reads through, so
// they check the actual JSON bytes on disk rather than in-memory state.
func permissionsPayload(t *testing.T, fs afero.Fs, settingsPath string) struct {
	Deny []string `json:"deny"`
	Ask  []string `json:"ask,omitempty"`
} {
	t.Helper()
	data, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)
	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
			Ask  []string `json:"ask,omitempty"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	return parsed.Permissions
}

// TestClaudeCodeHookWriter_WriteSettingsFile_DenyToolsLandInPermissions is the
// unit-level payload proof for the deny-tools fix: writeSettingsFile's
// denyTools parameter must appear verbatim in settings.json's
// permissions.deny — asserting the actual bytes on disk, not merely that the
// call returned nil (ctxloom's characteristic silent-no-op failure mode is
// exit 0 with zero bytes delivered).
func TestClaudeCodeHookWriter_WriteSettingsFile_DenyToolsLandInPermissions(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	require.NoError(t, writer.writeSettingsFile(&wire.HooksConfig{}, []string{"Task"}, "/project"))

	settingsPath := filepath.Join("/project", ".claude", "settings.json")
	perm := permissionsPayload(t, fs, settingsPath)
	assert.Equal(t, []string{"Task"}, perm.Deny, "the deny_tools payload must land verbatim in permissions.deny")
}

// TestClaudeCodeHookWriter_DenyTools_PreservesUserAllowAsk proves the deny
// merge does not clobber a user's own permissions.allow/ask entries — the
// same "reconcile without destroying user-authored config" invariant hooks
// and MCP servers already honor, extended to the new permissions surface.
func TestClaudeCodeHookWriter_DenyTools_PreservesUserAllowAsk(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	existing := map[string]interface{}{
		"permissions": map[string]interface{}{
			"allow": []string{"Bash(npm run lint)"},
			"ask":   []string{"WebFetch"},
		},
	}
	data, err := json.Marshal(existing)
	require.NoError(t, err)
	settingsPath := filepath.Join("/project", ".claude", "settings.json")
	require.NoError(t, afero.WriteFile(fs, settingsPath, data, 0644))

	require.NoError(t, writer.writeSettingsFile(&wire.HooksConfig{}, []string{"Task"}, "/project"))

	got, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)
	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(got, &parsed))
	perm := parsed["permissions"].(map[string]interface{})
	assert.ElementsMatch(t, []interface{}{"Bash(npm run lint)"}, perm["allow"], "user's allow rules must survive a deny_tools apply")
	assert.ElementsMatch(t, []interface{}{"WebFetch"}, perm["ask"], "user's ask rules must survive a deny_tools apply")
	assert.ElementsMatch(t, []interface{}{"Task"}, perm["deny"], "the ctxloom-managed deny entry must be added")
}

// TestClaudeCodeHookWriter_DenyTools_UnionsAcrossApplies proves two facts
// about the deny merge in one pass: (1) a re-apply with the SAME deny_tools
// does not duplicate the entry, and (2) a re-apply with an EMPTY deny_tools
// list does NOT retract a previously-written deny — the deliberate
// monotonic-add-only design (mergeDenyTools's doc): a denial is safe to keep,
// never safe to silently drop.
func TestClaudeCodeHookWriter_DenyTools_UnionsAcrossApplies(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}
	settingsPath := filepath.Join("/project", ".claude", "settings.json")

	require.NoError(t, writer.writeSettingsFile(&wire.HooksConfig{}, []string{"Task"}, "/project"))
	require.NoError(t, writer.writeSettingsFile(&wire.HooksConfig{}, []string{"Task"}, "/project"))
	assert.Equal(t, []string{"Task"}, permissionsPayload(t, fs, settingsPath).Deny, "re-applying the same deny_tools must not duplicate")

	// A later run whose resolved deny_tools is empty (e.g. the profile was
	// edited) must not erase the prior denial.
	require.NoError(t, writer.writeSettingsFile(&wire.HooksConfig{}, nil, "/project"))
	assert.Equal(t, []string{"Task"}, permissionsPayload(t, fs, settingsPath).Deny, "an empty deny_tools apply must NOT retract a previously-written deny")

	// A later run adding a second tool unions rather than replaces.
	require.NoError(t, writer.writeSettingsFile(&wire.HooksConfig{}, []string{"WebFetch"}, "/project"))
	assert.ElementsMatch(t, []string{"Task", "WebFetch"}, permissionsPayload(t, fs, settingsPath).Deny, "a later deny_tools entry unions with the existing deny list")
}

// U032-F03. The third half of taskloom lone-taste, left unfixed: an
// unparseable "permissions" block was warned about and then DELETED from the
// document. delete(raw, "permissions") ran unconditionally, outside the else,
// and saveSettings only re-emits permissions when the typed field is non-nil
// — so the user's allow/ask/defaultMode/additionalDirectories rules were
// dropped on the next write. That is a security surface, and unlike the
// whole-file and hooks cases there was no .corrupt backup either.
func TestClaudeCodeHookWriter_MalformedPermissionsJSON_FailsLoudAndBacksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	corruptContent := `{"permissions": "not-an-object", "env": {"KEEP": "me"}}`
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(corruptContent), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "./test.sh"}}},
	}
	err := writer.WriteSettings(cfg, nil, nil, "/project")
	require.Error(t, err, "should refuse to write when permissions in settings.json fail to parse")

	data, readErr := afero.ReadFile(fs, settingsPath)
	require.NoError(t, readErr)
	assert.Equal(t, corruptContent, string(data), "settings.json with unparseable permissions must not be overwritten")

	assert.True(t, hasCorruptBackup(t, fs, "/project/.claude", "settings.json", corruptContent),
		"expected a settings.json.corrupt-<timestamp> backup of the original bytes")
}

// The nested case: permissions parses as an object but permissions.deny is
// the wrong shape. Same rule — it must not cost the user their sibling rules.
func TestClaudeCodeHookWriter_MalformedPermissionsDeny_FailsLoudAndBacksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	corruptContent := `{"permissions": {"deny": "not-a-list", "allow": ["Bash(ls:*)"]}}`
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(corruptContent), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "./test.sh"}}},
	}
	err := writer.WriteSettings(cfg, nil, nil, "/project")
	require.Error(t, err, "should refuse to write when permissions.deny fails to parse")

	data, readErr := afero.ReadFile(fs, settingsPath)
	require.NoError(t, readErr)
	assert.Equal(t, corruptContent, string(data), "settings.json with unparseable permissions.deny must not be overwritten")
}

// A WELL-FORMED permissions block must still round-trip untouched: the guard
// must not make a healthy settings.json unwritable.
func TestClaudeCodeHookWriter_WellFormedPermissions_RoundTripUntouched(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	require.NoError(t, afero.WriteFile(fs, settingsPath,
		[]byte(`{"permissions": {"allow": ["Bash(ls:*)"], "defaultMode": "acceptEdits"}}`), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "./test.sh"}}},
	}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"Bash(ls:*)"`, "the user's allow rules must survive")
	assert.Contains(t, string(data), `"acceptEdits"`, "the user's defaultMode must survive")
}

// U032-F04. `delete(raw, "mcpServers")` destroyed a user's legacy mcpServers
// block. The comment called it a migration to .mcp.json, but no migration
// code exists — nothing ever reads that block. The struct field's own doc
// claims the opposite ("Preserve other settings (including legacy mcpServers
// for backwards compat)"). It also ran on the UNINSTALL path, so ctxloom
// destroyed them while being removed.
func TestClaudeCodeHookWriter_LegacyMCPServersInSettings_ArePreserved(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	settingsPath := "/project/.claude/settings.json"
	require.NoError(t, fs.MkdirAll("/project/.claude", 0755))
	require.NoError(t, afero.WriteFile(fs, settingsPath,
		[]byte(`{"mcpServers": {"mine": {"command": "my-server"}}}`), 0644))

	cfg := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{SessionStart: []wire.Hook{{Command: "./test.sh"}}},
	}
	require.NoError(t, writer.WriteSettings(cfg, nil, nil, "/project"))

	data, err := afero.ReadFile(fs, settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "my-server",
		"a legacy mcpServers block must not be deleted: nothing migrates it, so deleting it is pure loss")
}

// U032-F05. An unparseable .mcp.json was warned about and replaced with an
// EMPTY config, which writeMCPConfig then filled with ctxloom's servers and
// saved — the user's servers gone. The warning text even conceded "existing
// MCP servers may not be preserved". A warning is not a guard, and this is
// asymmetric with loadSettings, which was hardened for exactly this.
func TestClaudeCodeHookWriter_MalformedMCPConfig_FailsLoudAndBacksUp(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}

	mcpPath := "/project/.mcp.json"
	require.NoError(t, fs.MkdirAll("/project", 0755))
	corruptContent := `{"mcpServers": {"mine": {"command": "my-server"} `
	require.NoError(t, afero.WriteFile(fs, mcpPath, []byte(corruptContent), 0644))

	err := writer.writeMCPConfig("/project", &wire.MCPConfig{}, map[string]wire.MCPServer{
		"ctxloom-added": {Command: "ctxloom", Args: []string{"mcp"}},
	})
	require.Error(t, err, "should refuse to write .mcp.json over an unparseable one")

	data, readErr := afero.ReadFile(fs, mcpPath)
	require.NoError(t, readErr)
	assert.Equal(t, corruptContent, string(data), "the unparseable .mcp.json must not be overwritten")

	assert.True(t, hasCorruptBackup(t, fs, "/project", ".mcp.json", corruptContent),
		"expected a .mcp.json.corrupt-<timestamp> backup of the original bytes")
}

// An ABSENT .mcp.json is legitimately nothing to preserve and must still
// write cleanly.
func TestClaudeCodeHookWriter_AbsentMCPConfig_StillWrites(t *testing.T) {
	fs := afero.NewMemMapFs()
	writer := &ClaudeCodeHookWriter{FS: fs}
	require.NoError(t, fs.MkdirAll("/project", 0755))

	require.NoError(t, writer.writeMCPConfig("/project", &wire.MCPConfig{}, map[string]wire.MCPServer{
		"ctxloom-added": {Command: "ctxloom", Args: []string{"mcp"}},
	}))

	data, err := afero.ReadFile(fs, "/project/.mcp.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), "ctxloom-added")
}

// TestSaveSettings_UnencodableUserFieldIsRefusedNotDropped pins the invariant
// U032-F16 got backwards: saveSettings's "skip corrupted field" branch is NOT
// unreachable. settings.Other holds json.RawMessage slices lifted out of a
// document that parsed, so the bytes are always VALID JSON — but valid JSON is
// not the same as decodable into `any`: a number outside float64's range
// (`1e1000`) parses fine and then fails to unmarshal. The old warn+continue
// therefore deleted a real, user-authored key from settings.json on the way
// past, silently, on the same file whose loadSettings was hardened precisely
// because "a warning is not a guard".
//
// The payload assertion is on the FILE: a write that returns nil having
// dropped the key is the failure mode, so asserting the error alone would not
// catch a regression that writes and then errors.
func TestSaveSettings_UnencodableUserFieldIsRefusedNotDropped(t *testing.T) {
	fs := afero.NewMemMapFs()
	settingsPath := filepath.Join("/project", ".claude", "settings.json")
	original := `{"awkwardNumber": 1e1000, "env": {"A": "b"}}`
	require.NoError(t, fs.MkdirAll(filepath.Dir(settingsPath), 0755))
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(original), 0644))

	writer := &ClaudeCodeHookWriter{FS: fs}
	err := writer.writeSettingsFile(&wire.HooksConfig{}, nil, "/project")
	require.Error(t, err, "a setting ctxloom cannot re-encode must abort the write, not be dropped from it")

	data, readErr := afero.ReadFile(fs, settingsPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "awkwardNumber",
		"the user's own setting must still be in the file — dropping it is silent data loss")
}

// TestSaveSettings_UnencodablePermissionsSiblingIsRefusedNotDropped is the
// permissions-block twin of the above, and the worse half: the dropped sibling
// keys there are allow/ask/defaultMode — a SECURITY surface. loadSettings
// already refuses to proceed when it cannot PARSE that block; the write path
// must not quietly discard what the read path went to that trouble to keep.
func TestSaveSettings_UnencodablePermissionsSiblingIsRefusedNotDropped(t *testing.T) {
	fs := afero.NewMemMapFs()
	settingsPath := filepath.Join("/project", ".claude", "settings.json")
	original := `{"permissions": {"allow": ["Read"], "awkwardNumber": 1e1000}}`
	require.NoError(t, fs.MkdirAll(filepath.Dir(settingsPath), 0755))
	require.NoError(t, afero.WriteFile(fs, settingsPath, []byte(original), 0644))

	writer := &ClaudeCodeHookWriter{FS: fs}
	err := writer.writeSettingsFile(&wire.HooksConfig{}, []string{"Task"}, "/project")
	require.Error(t, err, "a permissions sibling ctxloom cannot re-encode must abort the write")

	data, readErr := afero.ReadFile(fs, settingsPath)
	require.NoError(t, readErr)
	assert.Contains(t, string(data), "awkwardNumber",
		"the user's own permissions rule must still be in the file")
	assert.Contains(t, string(data), "Read", "the surrounding allow list must be untouched too")
}

// denyStatFs fails Stat — and therefore afero.Exists — for the named paths
// with a non-IsNotExist error: the EACCES-on-the-parent-directory case, which
// afero.Exists reports as (false, err). Absent and unreadable are different
// answers and the writer must not conflate them.
type denyStatFs struct {
	afero.Fs
	deny map[string]error
}

func (f denyStatFs) Stat(name string) (os.FileInfo, error) {
	if err, ok := f.deny[name]; ok {
		return nil, err
	}
	return f.Fs.Stat(name)
}

// TestRemoveSettings_SettingsStatErrorIsLoud pins U032-F19 for the uninstall
// path: an unreadable settings.json must NOT be reported as "nothing to
// remove". Swallowing the stat error makes RemoveSettings a silent no-op that
// exits 0 while ctxloom's hooks and statusline stay installed — the user is
// told the uninstall succeeded and it did nothing.
func TestRemoveSettings_SettingsStatErrorIsLoud(t *testing.T) {
	settingsPath := filepath.Join("/project", ".claude", "settings.json")
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, settingsPath, []byte(`{"hooks":{}}`), 0644))
	fs := denyStatFs{Fs: base, deny: map[string]error{settingsPath: os.ErrPermission}}

	writer := &ClaudeCodeHookWriter{FS: fs}
	require.Error(t, writer.RemoveSettings("/project"),
		"an unreadable settings.json must fail the uninstall, not be treated as absent")
}

// TestRemoveSettings_MCPStatErrorIsLoud is the .mcp.json half of the same
// contract (settings.json is absent here, which is the one legitimate way to
// have nothing to remove).
func TestRemoveSettings_MCPStatErrorIsLoud(t *testing.T) {
	mcpPath := filepath.Join("/project", ".mcp.json")
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, mcpPath, []byte(`{"mcpServers":{}}`), 0644))
	fs := denyStatFs{Fs: base, deny: map[string]error{mcpPath: os.ErrPermission}}

	writer := &ClaudeCodeHookWriter{FS: fs}
	require.Error(t, writer.RemoveSettings("/project"),
		"an unreadable .mcp.json must fail the uninstall, not be treated as absent")
}

// TestStatus_SettingsStatErrorIsLoud pins the reporting half: Status is what
// `ctxloom` tells the user about their own installation, so answering "not
// installed" because the file could not be STATTED is a confident lie that
// invites a redundant install over live config.
func TestStatus_SettingsStatErrorIsLoud(t *testing.T) {
	settingsPath := filepath.Join("/project", ".claude", "settings.json")
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, settingsPath, []byte(`{"hooks":{}}`), 0644))
	fs := denyStatFs{Fs: base, deny: map[string]error{settingsPath: os.ErrPermission}}

	writer := &ClaudeCodeHookWriter{FS: fs}
	status, err := writer.Status("/project")
	require.Error(t, err, "an unreadable settings.json must be reported, not rendered as not-installed")
	assert.False(t, status.SettingsExists, "no claim about a file that could not be read")
}

// TestStatus_MCPStatErrorIsLoud is the .mcp.json half of the reporting
// contract.
func TestStatus_MCPStatErrorIsLoud(t *testing.T) {
	mcpPath := filepath.Join("/project", ".mcp.json")
	base := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(base, mcpPath, []byte(`{"mcpServers":{}}`), 0644))
	fs := denyStatFs{Fs: base, deny: map[string]error{mcpPath: os.ErrPermission}}

	writer := &ClaudeCodeHookWriter{FS: fs}
	_, err := writer.Status("/project")
	require.Error(t, err, "an unreadable .mcp.json must be reported, not rendered as not-installed")
}

// TestStatus_AbsentFilesAreNotAnError keeps the loud stat errors above from
// being satisfied the lazy way. A project with no settings.json and no
// .mcp.json is the ordinary not-installed answer and must stay silent.
func TestStatus_AbsentFilesAreNotAnError(t *testing.T) {
	writer := &ClaudeCodeHookWriter{FS: afero.NewMemMapFs()}
	status, err := writer.Status("/project")
	require.NoError(t, err)
	assert.False(t, status.SettingsExists)
	assert.False(t, status.MCPPresent)
}

// TestRemoveSettings_AbsentFilesAreNotAnError is the uninstall twin of the
// above: removing what was never installed stays a clean no-op.
func TestRemoveSettings_AbsentFilesAreNotAnError(t *testing.T) {
	writer := &ClaudeCodeHookWriter{FS: afero.NewMemMapFs()}
	require.NoError(t, writer.RemoveSettings("/project"))
}
