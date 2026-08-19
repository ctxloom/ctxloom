// Capability tests for the Claude Code launch backend (lifecycle, commands,
// context, history) plus the shared hook-assembly contract it drives.
package claude

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newClaudeLifecycle constructs the lifecycle the claude backend wires in
// NewClaudeCode: the shared BaseLifecycle for claude, which folds the
// host-assembled managed config into its merged hooks/MCP for the surface path.
func newClaudeLifecycle() *agent.BaseLifecycle {
	return agent.NewBaseLifecycle("claude-code")
}

func TestClaudeContext_GetContextHash(t *testing.T) {
	context := agent.NewBaseContextProvider()

	// Write context to set hash
	fragments := []*agent.Fragment{{Content: "test content"}}
	_ = context.Provide("/tmp", fragments)

	hash := context.GetContextHash()
	assert.NotEmpty(t, hash)
}

func TestClaudeContext_GetContextHash_Empty(t *testing.T) {
	context := agent.NewBaseContextProvider()

	hash := context.GetContextHash()
	assert.Equal(t, "", hash)
}

func TestClaudeContext_GetContextFilePath_Empty(t *testing.T) {
	context := agent.NewBaseContextProvider()

	path := context.GetContextFilePath()
	assert.Equal(t, "", path)
}

func TestClaudeContext_GetContextFilePath_WithHash(t *testing.T) {
	context := agent.NewBaseContextProvider()

	// Provide context to generate a hash
	tmpDir := t.TempDir()
	_ = context.Provide(tmpDir, []*agent.Fragment{{Content: "test content"}})

	path := context.GetContextFilePath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, agent.SCMContextSubdir)
	assert.Contains(t, path, ".md")
}

func TestClaudeContext_Clear(t *testing.T) {
	context := agent.NewBaseContextProvider()

	// Provide some context first
	_ = context.Provide("/tmp", []*agent.Fragment{{Content: "test"}})

	err := context.Clear("/tmp")
	require.NoError(t, err)
	assert.Equal(t, "", context.GetContextHash())
}

// The agent-side seam: MergeManaged folds the host-assembled (wire-typed)
// ManagedConfig into the lifecycle and appends the agent's own context-injection
// hook. The config/profile/bundle resolution that produces ManagedConfig is
// covered host-side in internal/lm/backends (managed_test.go).

func TestClaudeLifecycle_MergeManaged_AppendsContextInjection(t *testing.T) {
	lifecycle := newClaudeLifecycle()

	lifecycle.MergeManaged(&agent.ManagedConfig{
		Hooks: &wire.HooksConfig{Plugins: map[string]wire.BackendHooks{}},
	}, "/tmp", "abc123hash")

	hooks := lifecycle.GetHooks()
	var hasInject bool
	for _, h := range hooks.Unified.SessionStart {
		if strings.Contains(h.Command, "inject-context") {
			hasInject = true
		}
	}
	assert.True(t, hasInject, "context-injection hook must be appended when contextHash is set")
}

func TestClaudeLifecycle_MergeManaged_NoContextHash(t *testing.T) {
	lifecycle := newClaudeLifecycle()

	// Host-assembled SessionStart hooks (e.g. bundle `hook session-bind`) ride in
	// via ManagedConfig.Hooks; the agent appends only the context-injection hook,
	// and only when a hash is present.
	lifecycle.MergeManaged(&agent.ManagedConfig{
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind"}},
			},
			Plugins: map[string]wire.BackendHooks{},
		},
	}, "/tmp", "")

	hooks := lifecycle.GetHooks()
	for _, h := range hooks.Unified.SessionStart {
		assert.NotContains(t, h.Command, "inject-context",
			"context-injection hook must not be added when contextHash is empty")
	}
	var hasBind bool
	for _, h := range hooks.Unified.SessionStart {
		if strings.Contains(h.Command, "session-bind") {
			hasBind = true
		}
	}
	assert.True(t, hasBind, "host-assembled SessionStart hooks must be preserved")
}

func TestClaudeLifecycle_MergeManaged_MergesHooksAndMCP(t *testing.T) {
	lifecycle := newClaudeLifecycle()

	lifecycle.MergeManaged(&agent.ManagedConfig{
		Hooks: &wire.HooksConfig{
			Unified: wire.UnifiedHooks{PreTool: []wire.Hook{{Command: "profile-hook"}}},
			Plugins: map[string]wire.BackendHooks{},
		},
		BundleMCP: map[string]wire.MCPServer{"profile-mcp": {Command: "profile-mcp-cmd"}},
	}, "/tmp", "hash123")

	hooks := lifecycle.GetHooks()
	assert.Len(t, hooks.Unified.PreTool, 1)
	assert.Equal(t, "profile-hook", hooks.Unified.PreTool[0].Command)

	assert.Contains(t, lifecycle.GetBundleMCP(), "profile-mcp")
}

// TestClaudeLifecycle_MergeManaged_Statusline verifies the ManageStatusline bit
// drives the settings surface end-to-end through the real settings.json write:
// true installs the ctxloom statusline, false omits it. The settings surface (not
// the lifecycle) owns this write now that delivery rides the surfaces × cells
// seam; the surface receives the bit from SurfaceInputs.ManageStatusline.
func TestClaudeLifecycle_MergeManaged_Statusline(t *testing.T) {
	deliverSettings := func(t *testing.T, manage bool) string {
		t.Helper()
		fs := afero.NewMemMapFs()
		surfaces := NewSurfaces(agent.SurfaceInputs{Hooks: &wire.HooksConfig{}, ManageStatusline: manage}, dirPlacement{}, fs)
		_, err := surfaces.Settings.Deliver("/proj")
		require.NoError(t, err)
		data, err := afero.ReadFile(fs, filepath.Join("/proj", ".claude", "settings.json"))
		require.NoError(t, err)
		return string(data)
	}

	t.Run("managed installs statusline", func(t *testing.T) {
		assert.Contains(t, deliverSettings(t, true), "statusLine", "ManageStatusline=true must write a ctxloom statusline")
	})

	t.Run("opt-out omits statusline", func(t *testing.T) {
		assert.NotContains(t, deliverSettings(t, false), "statusLine", "ManageStatusline=false must not write a statusline")
	})
}

func TestClaudeLifecycle_MergeManaged_NilIsNoOp(t *testing.T) {
	lifecycle := newClaudeLifecycle()
	lifecycle.MergeManaged(nil, "/tmp", "hash123") // must not panic
	assert.Nil(t, lifecycle.GetHooks(), "nil managed config must not initialize hook state")
}

func TestClaudeLifecycle_GetMCP(t *testing.T) {
	lifecycle := newClaudeLifecycle()

	// Initially nil
	assert.Nil(t, lifecycle.GetBundleMCP())

	// After merging a managed config carrying MCP servers.
	lifecycle.MergeManaged(&agent.ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{"test-server": {Command: "test"}},
	}, "/tmp", "")

	assert.NotNil(t, lifecycle.GetBundleMCP())
}

func TestClaudeCode_History(t *testing.T) {
	// tough-cloud S5: claude's SessionHistory scraper (~/.claude/projects/*
	// .jsonl) was deleted outright (tall-grab: wrong-filename resolution);
	// History() is nil now that canonical capture is the only transcript
	// source for claude.
	backend := NewClaudeCode()
	history := backend.History()
	assert.Nil(t, history, "session history scraper retired, tough-cloud S5")
}
