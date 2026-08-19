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

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deliverManagedSettings materializes a backend's settings + MCP surfaces into dir
// via the surface selection — the test replacement for the removed WriteSettings
// facade. manageStatusline mirrors the old WithStatusLineDisabled inverse (the old
// facade default, with no opt, MANAGED the statusline, so pass true there).
func deliverManagedSettings(t *testing.T, backend string, hooks *wire.HooksConfig, bundleMCP map[string]wire.MCPServer, manageStatusline bool, dir string, fs afero.Fs) {
	t.Helper()
	set := BuildSurfaces(backend, agent.SurfaceInputs{
		Hooks:            hooks,
		BundleMCP:        bundleMCP,
		ManageStatusline: manageStatusline,
	}, fs)
	_, _, errs := agent.Select(set).WithSettings(agent.SettingsWriteUnsafeFile).WithMCP(agent.MCPWriteUnsafeFile).DeliverUnder(dir)
	require.Empty(t, errs)
}

// =============================================================================
// Hash Computation Tests
// =============================================================================
// Hash-based identification enables ctxloom to track which hooks it manages vs
// user-defined hooks, allowing clean updates without losing user customization.

// TestNewContextInjectionHook_ShellQuotesProjectPath pins the shell-safe
// quoting of the --project path: spaces, single quotes, and shell
// metacharacters must not break the command split or inject behavior when
// /bin/sh runs the hook.
func TestNewContextInjectionHook_ShellQuotesProjectPath(t *testing.T) {
	h := agent.NewContextInjectionHook("hash1", "/tmp/My Project")
	assert.Contains(t, h.Command, `--project '/tmp/My Project' hash1`,
		"path with spaces must be single-quoted; got %q", h.Command)

	h = agent.NewContextInjectionHook("hash2", "/tmp/it's mine")
	assert.Contains(t, h.Command, `--project '/tmp/it'\''s mine' hash2`,
		"embedded single quote must use the '\\'' idiom; got %q", h.Command)

	// The binary itself now names the self-exec absolute path
	// (agent.CtxloomCommand), not the bare "ctxloom" — see its doc for the
	// staged-vs-installed invariant this upholds.
	assert.Contains(t, h.Command, agent.CtxloomCommand(),
		"command must name the self-exec absolute path; got %q", h.Command)
	assert.Contains(t, h.Command, " hook inject-context ",
		"command must invoke the inject-context subcommand; got %q", h.Command)
	assert.True(t, agent.IsManaged(h.Command, "ctxloom"),
		"self-exec absolute path must still resolve to the ctxloom exec token; got %q", h.Command)
}

// TestNewContextInjectionHooks_ChunksLargeContext verifies that a large
// content-addressed context file is split into N ordered chunk hooks
// (--part k --of N), while small or missing content yields a single
// whole-content hook (the legacy, backward-compatible form).
func TestNewContextInjectionHooks_ChunksLargeContext(t *testing.T) {
	writeCtxFile := func(t *testing.T, workDir, hash, content string) {
		t.Helper()
		dir := filepath.Join(workDir, agent.SCMContextSubdir)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, hash+".md"), []byte(content), 0o644))
	}

	t.Run("small_content_single_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		writeCtxFile(t, tmpDir, "smallhash", "# tiny\nbody")
		hooks := agent.NewContextInjectionHooks("smallhash", tmpDir)
		require.Len(t, hooks, 1)
		assert.NotContains(t, hooks[0].Command, "--part",
			"single chunk must use the legacy whole-content form")
	})

	t.Run("missing_file_single_hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		hooks := agent.NewContextInjectionHooks("nofile", tmpDir)
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

		hooks := agent.NewContextInjectionHooks("bighash", tmpDir)
		n := len(hooks)
		require.Greater(t, n, 1, "large content must split into multiple chunk hooks")
		for k, h := range hooks {
			assert.Containsf(t, h.Command, fmt.Sprintf("--part %d --of %d", k+1, n),
				"hook %d must be the (k+1)-th of n in order; got %q", k, h.Command)
			assert.Truef(t, agent.IsManaged(h.Command, "ctxloom"),
				"chunk hook must be recognized as ctxloom-managed; got %q", h.Command)
			assert.Equal(t, agent.ContextInjectionTimeout, h.Timeout)
		}
	})
}

// (Managed-command detection is exercised in shared/agent — TestIsManaged in
// predicate_test.go — now that isCtxloomManaged is a thin agent.IsManaged call.)

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
		{"codex", "codex", true},      // config.toml hooks + MCP
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

func TestBuildSurfaces_UnsupportedBackend(t *testing.T) {
	// Unsupported backends materialize no surfaces (EmptySurfaceSet), so a full
	// selection delivers nothing and reports no errors — the opt-out no-op the old
	// WriteSettings dispatch gave.
	set := BuildSurfaces("unknown-backend", agent.SurfaceInputs{}, afero.NewMemMapFs())
	_, kinds, errs := agent.Select(set).WithEverything().DeliverUnder("/project")
	assert.Empty(t, errs)
	assert.Empty(t, kinds)
}

func TestDeliverManagedSettings_WithFS(t *testing.T) {
	fs := afero.NewMemMapFs()

	hooks := &wire.HooksConfig{
		Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "./test.sh"}},
		},
	}

	deliverManagedSettings(t, "claude-code", hooks, nil, true, "/project", fs)

	// Verify settings were written
	exists, _ := afero.Exists(fs, "/project/.claude/settings.json")
	assert.True(t, exists)
}

// =============================================================================
// Schema Resilience Tests
// =============================================================================
// These tests verify that ctxloom gracefully handles malformed or incompatible
// settings.json files, as Claude Code's schema is undocumented and may change.

// (AtomicWriteFile / GetFS / ComputeHookHash are covered in shared/agent —
// settings_io_test.go — alongside the helpers themselves.)
//
// TestClaudeCodeHookWriter_WritesSelfExecAbsoluteCommands proves the
// staged-binary-divergence fix end to end: every surface this writer
// materializes (statusline, inject-context hook, auto-registered MCP
// server) names the self-exec absolute path (agent.CtxloomCommand) of the
// binary running THIS test, not the bare "ctxloom" that used to re-resolve
// against PATH at fire time. Bundle-shipped hooks are untouched — their
// Command is author-supplied, never materialized by this binary.
func TestClaudeCodeHookWriter_WritesSelfExecAbsoluteCommands(t *testing.T) {
	tmpDir := t.TempDir()
	self := agent.CtxloomCommand()

	writer := &claude.ClaudeCodeHookWriter{}
	// Inject-context hook is constructed exactly the way the lifecycle
	// constructs it — through the public constructor.
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{agent.NewContextInjectionHook("abc123", tmpDir)},
		PostFileEdit: []wire.Hook{
			{Command: "ctxloom hook stamp-plan", Type: "command"},
		},
	}}
	require.NoError(t, writer.WriteSettings(cfg, map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}}, tmpDir))

	settingsData, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	statusLine := settings["statusLine"].(map[string]any)
	assert.Equal(t, self+" hook hud", statusLine["command"],
		"statusLine must name the self-exec absolute path; got %q", statusLine["command"])

	hooks := settings["hooks"].(map[string]any)

	sessionStart := hooks["SessionStart"].([]any)
	require.NotEmpty(t, sessionStart)
	injectCmd := sessionStart[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.Contains(t, injectCmd, self,
		"inject-context hook must name the self-exec absolute path; got %q", injectCmd)
	assert.True(t, agent.IsManaged(injectCmd, "ctxloom"),
		"self-exec absolute path must still resolve to the ctxloom exec token; got %q", injectCmd)

	post := hooks["PostToolUse"].([]any)
	require.NotEmpty(t, post)
	bundleCmd := post[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.Equal(t, "ctxloom hook stamp-plan", bundleCmd,
		"bundle-shipped hook command is author-supplied, not materialized by this binary; got %q", bundleCmd)

	// .mcp.json: ctxloom's own MCP server command names the self-exec absolute
	// path too — the bundle declares the bare name, the writer resolves it.
	mcpData, err := os.ReadFile(filepath.Join(tmpDir, ".mcp.json"))
	require.NoError(t, err)
	var mcpConfig map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcpConfig))
	ctxloomServer := mcpConfig["mcpServers"].(map[string]any)["ctxloom"].(map[string]any)
	assert.Equal(t, self, ctxloomServer["command"],
		"ctxloom's own MCP server command must name the self-exec absolute path; got %q", ctxloomServer["command"])
}
