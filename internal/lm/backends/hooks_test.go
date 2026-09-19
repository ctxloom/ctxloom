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

	"github.com/ctxloom/ctxloom/internal/shared/exectoken"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
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
	sel := agent.Select(Declared(backend)).With(agent.SurfaceSettings, agent.ApproachUnsafeFile).With(agent.SurfaceMCP, agent.ApproachUnsafeFile)
	_, _, errs := sel.DeliverUnder(agent.SurfaceInputs{
		Hooks:            hooks,
		BundleMCP:        bundleMCP,
		ManageStatusline: manageStatusline,
	}, fs, present.ProjectOnHost(dir))
	require.Empty(t, errs)
}

// =============================================================================
// Hash Computation Tests
// =============================================================================
// Hash-based identification enables ctxloom to track which hooks it manages vs
// user-defined hooks, allowing clean updates without losing user customization.

// TestNewContextInjectionHook_CarriesNoMachineFact pins the portability
// invariant on the generated command: neither the binary nor a project path
// may be a fact about the machine that wrote it. The generated settings file
// is tracked, so a path here is committed and every other clone gets a hook
// that fails silently. The project is resolved at FIRE time instead, by
// cli.resolveInjectContextWorkDir.
//
// MUTATION — re-add `--project <abs>` to the emitted command, or return an
// absolute path from agent.CtxloomCommand; both must go RED.
func TestNewContextInjectionHook_CarriesNoMachineFact(t *testing.T) {
	h := agent.NewContextInjectionHook("hash1")

	assert.NotContains(t, h.Command, "--project",
		"the generated hook must not embed a project path; got %q", h.Command)
	assert.Equal(t, "'ctxloom' hook inject-context hash1", h.Command)
	assert.True(t, exectoken.IsManaged(h.Command, "ctxloom"),
		"the quoted bare name must still resolve to the ctxloom exec token; got %q", h.Command)
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
			assert.Truef(t, exectoken.IsManaged(h.Command, "ctxloom"),
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
		// mock is a complete engine and carries a real settings writer. The
		// case that catches a factory handing back a writer for anything it
		// recognizes is the UNKNOWN name below, which is the honest test for
		// it — mock stopped being that case when it gained the capability.
		{"mock", "mock", true},
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

func TestDeclared_UnsupportedBackend(t *testing.T) {
	// Unsupported backends declare no surfaces (an empty Declaration), so a
	// full selection delivers nothing and reports no errors — the opt-out
	// no-op the old WriteSettings dispatch gave.
	_, kinds, errs := agent.Select(Declared("unknown-backend")).WithEverything().DeliverUnder(agent.SurfaceInputs{}, afero.NewMemMapFs(), present.ProjectOnHost("/project"))
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
// TestClaudeCodeHookWriter_WritesNoAbsolutePaths proves the portability fix
// end to end: every surface this writer materializes (statusline,
// inject-context hook, auto-registered MCP server) names the BARE `ctxloom`,
// resolved on PATH at fire time. .claude/settings.json is a tracked file, so
// an absolute path in any of them is one developer's machine committed into
// the repo — and every other clone then runs hooks that silently do nothing.
// Bundle-shipped hooks are untouched — their Command is author-supplied,
// never materialized by this binary.
//
// MUTATION — materialize any of the three from selfexec.Path(); RED.
func TestClaudeCodeHookWriter_WritesNoAbsolutePaths(t *testing.T) {
	tmpDir := t.TempDir()

	writer := &claude.ClaudeCodeHookWriter{}
	// Inject-context hook is constructed exactly the way the lifecycle
	// constructs it — through the public constructor.
	cfg := &wire.HooksConfig{Unified: wire.UnifiedHooks{
		SessionStart: []wire.Hook{agent.NewContextInjectionHook("abc123")},
		PostFileEdit: []wire.Hook{
			{Command: "ctxloom hook stamp-plan", Type: "command"},
		},
	}}
	require.NoError(t, writer.WriteSettings(cfg, map[string]wire.MCPServer{agent.MCPServerName: {Command: agent.CtxloomBinary, Args: []string{"mcp", "serve"}}}, tmpDir))

	settingsData, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(settingsData, &settings))

	assert.NotContains(t, string(settingsData), "/",
		"no path component may appear anywhere in a tracked settings.json; got %s", settingsData)

	statusLine := settings["statusLine"].(map[string]any)
	assert.Equal(t, agent.CtxloomBinary+" hook hud", statusLine["command"],
		"statusLine must name the bare ctxloom; got %q", statusLine["command"])

	hooks := settings["hooks"].(map[string]any)

	sessionStart := hooks["SessionStart"].([]any)
	require.NotEmpty(t, sessionStart)
	injectCmd := sessionStart[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.NotContains(t, injectCmd, "--project",
		"the materialized inject-context hook must carry no project path; got %q", injectCmd)
	assert.True(t, exectoken.IsManaged(injectCmd, "ctxloom"),
		"the bare name must still resolve to the ctxloom exec token; got %q", injectCmd)

	post := hooks["PostToolUse"].([]any)
	require.NotEmpty(t, post)
	bundleCmd := post[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.Equal(t, "ctxloom hook stamp-plan", bundleCmd,
		"bundle-shipped hook command is author-supplied, not materialized by this binary; got %q", bundleCmd)

	// .mcp.json: ctxloom's own MCP server command is the bare name too — a
	// container reads this file at an identical path and must be able to exec it.
	mcpData, err := os.ReadFile(filepath.Join(tmpDir, ".mcp.json"))
	require.NoError(t, err)
	var mcpConfig map[string]any
	require.NoError(t, json.Unmarshal(mcpData, &mcpConfig))
	ctxloomServer := mcpConfig["mcpServers"].(map[string]any)["ctxloom"].(map[string]any)
	assert.Equal(t, agent.CtxloomBinary, ctxloomServer["command"],
		"ctxloom's own MCP server command must be the bare name; got %q", ctxloomServer["command"])
}
