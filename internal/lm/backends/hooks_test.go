// Hooks tests verify that ctxloom correctly manages hooks and MCP servers in
// backend configuration files. This is critical for the context injection
// system - hooks enable ctxloom to inject context at session start, and MCP
// servers expose ctxloom's tools to AI assistants. Tests ensure user-defined
// settings are preserved while ctxloom-managed ones are updated.
package backends

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agent/claude"
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

// TestNewContextInjectionHook_ShellQuotesProjectPath pins the shell-safe
// quoting of the --project path: spaces, single quotes, and shell
// metacharacters must not break the command split or inject behavior when
// /bin/sh runs the hook.
func TestNewContextInjectionHook_ShellQuotesProjectPath(t *testing.T) {
	h := NewContextInjectionHook("hash1", "/tmp/My Project")
	assert.Contains(t, h.Command, `--project '/tmp/My Project' hash1`,
		"path with spaces must be single-quoted; got %q", h.Command)

	h = NewContextInjectionHook("hash2", "/tmp/it's mine")
	assert.Contains(t, h.Command, `--project '/tmp/it'\''s mine' hash2`,
		"embedded single quote must use the '\\'' idiom; got %q", h.Command)

	assert.True(t, strings.HasPrefix(h.Command, "ctxloom hook inject-context "),
		"bare-ctxloom prefix invariant must hold; got %q", h.Command)
}

// TestNewContextInjectionHooks_ChunksLargeContext verifies that a large
// content-addressed context file is split into N ordered chunk hooks
// (--part k --of N), while small or missing content yields a single
// whole-content hook (the legacy, backward-compatible form).
func TestNewContextInjectionHooks_ChunksLargeContext(t *testing.T) {
	writeCtxFile := func(t *testing.T, workDir, hash, content string) {
		t.Helper()
		dir := filepath.Join(workDir, SCMContextSubdir)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, hash+".md"), []byte(content), 0o644))
	}

	t.Run("small_content_single_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		writeCtxFile(t, tmpDir, "smallhash", "# tiny\nbody")
		hooks := NewContextInjectionHooks("smallhash", tmpDir)
		require.Len(t, hooks, 1)
		assert.NotContains(t, hooks[0].Command, "--part",
			"single chunk must use the legacy whole-content form")
	})

	t.Run("missing_file_single_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooks := NewContextInjectionHooks("nofile", tmpDir)
		require.Len(t, hooks, 1)
		assert.NotContains(t, hooks[0].Command, "--part",
			"missing file degrades to a single whole-content hook")
	})

	t.Run("large_content_ordered_chunks", func(t *testing.T) {
		tmpDir := t.TempDir()
		var sections []string
		for i := range 6 {
			sections = append(sections, "# Section "+string(rune('A'+i))+"\n"+strings.Repeat("x", 3000))
		}
		writeCtxFile(t, tmpDir, "bighash", strings.Join(sections, "\n\n---\n\n"))

		hooks := NewContextInjectionHooks("bighash", tmpDir)
		n := len(hooks)
		require.Greater(t, n, 1, "large content must split into multiple chunk hooks")
		for k, h := range hooks {
			assert.Containsf(t, h.Command, fmt.Sprintf("--part %d --of %d", k+1, n),
				"hook %d must be the (k+1)-th of n in order; got %q", k, h.Command)
			assert.Truef(t, isCtxloomManaged(h.Command),
				"chunk hook must be recognized as ctxloom-managed; got %q", h.Command)
			assert.Equal(t, ContextInjectionTimeout, h.Timeout)
		}
	})
}

// TestIsCtxloomManaged covers the single predicate driving cleanup across
// hooks, the statusLine, and the MCP server. Any command whose executable
// is `ctxloom` is ours, regardless of verb — so a slot whose verb drifted
// (the legacy `ctxloom hook hud` statusLine) still migrates forward instead
// of being mistaken for user-authored.
func TestIsCtxloomManaged(t *testing.T) {
	cases := map[string]bool{
		// All ctxloom invocations are managed — hooks, statusLine, MCP.
		"ctxloom hook inject-context --project /p hash": true,
		"ctxloom hook session-bind":                     true,
		"ctxloom hook stamp-plan":                       true,
		"ctxloom hook hud":                              true,
		"ctxloom mcp":                                   true,
		// Legacy verb forms (pre-callback-consolidation) must still migrate forward.
		"ctxloom session bind":     true,
		"ctxloom tasks stamp-plan": true,
		"ctxloom meta hud":         true,
		// Quoted / absolute / Windows executable paths.
		`"/usr/bin/ctxloom" hook hud`:               true,
		"/home/me/go/bin/ctxloom hook session-bind": true,
		`"C:\Tools\ctxloom.exe" hook stamp-plan`:    true,
		// Quoted executable path containing spaces — strings.Fields used to
		// split this mid-path and miss it, leaving dup hooks to accumulate.
		`"/Apps/My Tools/ctxloom" mcp`:             true,
		`'/Apps/My Tools/ctxloom' hook stamp-plan`: true,
		// Not ctxloom.
		"echo 'user hook'":                   false,
		"node /opt/somewhere/script.js":      false,
		"/usr/local/bin/ctxloomctl whatever": false,
		"starship prompt":                    false,
		"":                                   false,
	}
	for cmd, want := range cases {
		if got := isCtxloomManaged(cmd); got != want {
			t.Errorf("isCtxloomManaged(%q) = %v; want %v", cmd, got, want)
		}
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
		{"codex", "codex", false},     // No settings support
		{"unknown", "unknown", false}, // Unknown backend
		{"empty", "", false},          // Empty string
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
func TestClaudeCodeHookWriter_WritesBareCtxloomCommands(t *testing.T) {
	tmpDir := t.TempDir()
	SetExecutablePathForTesting("/install/now/ctxloom")
	t.Cleanup(func() { SetExecutablePathForTesting("") })

	writer := &claude.ClaudeCodeHookWriter{}
	// Inject-context hook is constructed exactly the way the lifecycle
	// constructs it — through the public constructor.
	cfg := &config.HooksConfig{Unified: config.UnifiedHooks{
		SessionStart: []config.Hook{NewContextInjectionHook("abc123", tmpDir)},
		PostFileEdit: []config.Hook{
			{Command: "ctxloom hook stamp-plan", Type: "command"},
		},
	}}
	require.NoError(t, writer.WriteHooks(cfg, tmpDir))

	settingsData, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	statusLine := settings["statusLine"].(map[string]any)
	assert.Equal(t, "ctxloom hook hud", statusLine["command"],
		"statusLine must be bare; got %q", statusLine["command"])

	hooks := settings["hooks"].(map[string]any)

	sessionStart := hooks["SessionStart"].([]any)
	require.NotEmpty(t, sessionStart)
	injectCmd := sessionStart[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.True(t, strings.HasPrefix(injectCmd, "ctxloom hook inject-context "),
		"inject-context hook must be bare; got %q", injectCmd)

	post := hooks["PostToolUse"].([]any)
	require.NotEmpty(t, post)
	bundleCmd := post[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.Equal(t, "ctxloom hook stamp-plan", bundleCmd,
		"bundle-shipped hook must stay bare; got %q", bundleCmd)

	// .mcp.json: auto-registered ctxloom MCP server command is bare too.
	mcpData, err := os.ReadFile(filepath.Join(tmpDir, ".mcp.json"))
	require.NoError(t, err)
	var mcpConfig map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcpConfig))
	ctxloomServer := mcpConfig["mcpServers"].(map[string]any)["ctxloom"].(map[string]any)
	assert.Equal(t, "ctxloom", ctxloomServer["command"],
		"auto-registered MCP server command must be bare; got %q", ctxloomServer["command"])
}
