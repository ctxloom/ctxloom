// Lifecycle tests cover the host→backend setup seam: MergeManaged folds the
// host-assembled ManagedConfig into the lifecycle, and Flush writes the
// accumulated hooks + MCP servers to the backend's settings. The bundle-MCP
// cases pin the invariant that the launch-path write is symmetric with
// operations.ApplyHooks — both must carry profile/builtin bundle servers, or
// WriteSettings' remove-all-then-re-add reconcile silently drops whatever one
// writer assembled but the other didn't.
package agent

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureWriteSettings records the arguments of the last WriteSettings call so
// tests can assert what the lifecycle hands the backend writer.
type captureWriteSettings struct {
	called    bool
	hooks     *wire.HooksConfig
	mcp       *wire.MCPConfig
	bundleMCP map[string]wire.MCPServer
}

func (c *captureWriteSettings) fn() WriteSettingsFunc {
	return func(_ string, hooks *wire.HooksConfig, mcp *wire.MCPConfig, bundleMCP map[string]wire.MCPServer, _ string, _ ...SettingsOption) error {
		c.called = true
		c.hooks = hooks
		c.mcp = mcp
		c.bundleMCP = bundleMCP
		return nil
	}
}

// Flush must pass the bundle MCP servers MergeManaged folded in through to the
// writer. Passing nil here (the original launch-path behavior) makes the
// remove-all-then-re-add reconcile strip every bundle server (taskloom,
// sequential-thinking, …) from .mcp.json on every launch, so the booting
// session never sees them.
func TestBaseLifecycle_Flush_PassesBundleMCP(t *testing.T) {
	cap := &captureWriteSettings{}
	l := NewBaseLifecycle("claude-code", cap.fn())

	l.MergeManaged(&ManagedConfig{
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{}},
		BundleMCP: map[string]wire.MCPServer{
			"taskloom": {Command: "taskloom", Args: []string{"mcp"}},
		},
	}, "/work", "")

	require.NoError(t, l.Flush("/work"))

	require.True(t, cap.called, "Flush must invoke writeSettings")
	require.NotNil(t, cap.bundleMCP, "Flush dropped bundle MCP servers (passed nil)")
	assert.Contains(t, cap.bundleMCP, "taskloom",
		"launch-path Flush must carry bundle servers, matching operations.ApplyHooks")
}

// With no managed state at all, Flush is a no-op and must not call the writer.
func TestBaseLifecycle_Flush_NoopWhenEmpty(t *testing.T) {
	cap := &captureWriteSettings{}
	l := NewBaseLifecycle("claude-code", cap.fn())

	require.NoError(t, l.Flush("/work"))
	assert.False(t, cap.called, "empty lifecycle must not write settings")
}

// Bundle servers supplied without hooks or unified MCP must still trigger a
// write — otherwise the empty-state guard would swallow a bundle-only set.
func TestBaseLifecycle_Flush_WritesBundleOnly(t *testing.T) {
	cap := &captureWriteSettings{}
	l := NewBaseLifecycle("claude-code", cap.fn())

	l.MergeManaged(&ManagedConfig{
		BundleMCP: map[string]wire.MCPServer{
			"taskloom": {Command: "taskloom", Args: []string{"mcp"}},
		},
	}, "/work", "")

	require.NoError(t, l.Flush("/work"))
	require.True(t, cap.called, "bundle-only managed set must still write")
	assert.Contains(t, cap.bundleMCP, "taskloom")
}
