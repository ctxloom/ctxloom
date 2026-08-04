//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bundle that ships both an MCP server and a hook. The bug class under test
// silently dropped a bundle's MCP server / hooks when the bundle was reachable
// only through profile inheritance (spotty-unstirred-anatomist).
const applyDemoBundleYAML = `name: demo
version: "1.0"
mcp:
  demo-server:
    command: demo-mcp
    args: ["--stdio"]
hooks:
  session_start:
    - command: echo demo-hook
      type: command
`

// applyHooksForProfile lays out an .ctxloom app dir with the demo bundle and the
// given profiles, makes defaultProfile the active default, runs operations.ApplyHooks
// against a fresh project dir, and returns the written settings files
// (Claude's .mcp.json, Antigravity's hooks.json + mcp_config.json, and
// Claude's settings.json).
func applyHooksForProfile(t *testing.T, defaultProfile string, profiles map[string]string) (mcpJSON, agyHooksJSON, agyMCPJSON, claudeJSON string) {
	t.Helper()

	// This helper runs IN-PROCESS against the developer's real PATH and real
	// home, and the `session-bind` hook these tests assert on ships in
	// TASKLOOM's loadout (cmd/taskloom/loadout.yaml), not in an embedded
	// bundle — so the assertions already depend on a real taskloom being
	// installed. Pin the exec-consent gate open so they do not ALSO depend on
	// whether this machine's ~/.ctxloom/companion_consent.yaml happens to have
	// approved it: the subject here is hook diversion, not admission, and
	// admission has its own tests.
	defer config.AdmitEveryDiscoveredCompanionForTesting()()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "content", "bundles") // paths.LocalBundlesPath layout
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "demo.yaml"), []byte(applyDemoBundleYAML), 0o644))
	for name, content := range profiles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(content), 0o644))
	}

	projectDir := t.TempDir()
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{defaultProfile}}},
		AppPaths:     []string{appDir},
	})

	_, err := operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		Backend:      "all",
		WorkDir:      projectDir,
		FS:           afero.NewOsFs(),
		ConfigLoader: func() (*config.Config, error) { return cfg, nil },
	})
	require.NoError(t, err)

	return readOrEmpty(t, filepath.Join(projectDir, ".mcp.json")),
		readOrEmpty(t, filepath.Join(projectDir, ".agents", "hooks.json")),
		readOrEmpty(t, filepath.Join(projectDir, ".agents", "mcp_config.json")),
		readOrEmpty(t, filepath.Join(projectDir, ".claude", "settings.json"))
}

func readOrEmpty(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

// assertBundleApplied checks the demo bundle's MCP server reached the Claude
// (.mcp.json) and Antigravity (.agents/mcp_config.json) stores, and its hook
// reached the Claude settings.
func assertBundleApplied(t *testing.T, mcpJSON, agyMCPJSON, claudeJSON string) {
	t.Helper()
	assert.Contains(t, mcpJSON, "demo-server", "MCP server must land in .mcp.json")
	assert.Contains(t, mcpJSON, "demo-mcp", "MCP server command must land in .mcp.json")
	assert.Contains(t, agyMCPJSON, "demo-server", "MCP server must land in .agents/mcp_config.json")
	assert.Contains(t, claudeJSON, "demo-hook", "bundle hook must land in .claude/settings.json")
}

// TestBundleApply_DirectProfile: the demo bundle is referenced directly by the
// default profile. Baseline that MCP + hooks flow through apply.
func TestBundleApply_DirectProfile(t *testing.T) {
	mcpJSON, _, agyMCPJSON, claudeJSON := applyHooksForProfile(t, "base", map[string]string{
		"base": "name: base\nbundles:\n  - demo\n",
	})
	assertBundleApplied(t, mcpJSON, agyMCPJSON, claudeJSON)
}

// TestBundleApply_InheritedProfile: the demo bundle is referenced only by a
// parent profile; the default ("child") merely inherits it. This is the
// spotty-unstirred-anatomist regression driven end-to-end through ApplyHooks:
// before the fix, inherited bundles' MCP servers and hooks were silently dropped.
func TestBundleApply_InheritedProfile(t *testing.T) {
	mcpJSON, _, agyMCPJSON, claudeJSON := applyHooksForProfile(t, "child", map[string]string{
		"parent": "name: parent\nbundles:\n  - demo\n",
		"child":  "name: child\nparents:\n  - parent\n",
	})
	assertBundleApplied(t, mcpJSON, agyMCPJSON, claudeJSON)
}

// TestBundleApply_AntigravityHookNestedSchema drives ApplyHooks end-to-end and
// asserts bundle hooks reach .agents/hooks.json in agy's required nested shape
// (event → [{hooks:[{type:"command", command}]}]). A flat {command} object is
// silently ignored by agy, so this is the contract that makes Antigravity
// hooks actually fire. The built-in `hook session-bind` (the recovery
// producer) declares pre_tool_fallback, so on agy it must land under
// PreToolUse with a catch-all matcher — agy never fires SessionStart, and a
// SessionStart registration would be a dead entry.
func TestBundleApply_AntigravityHookNestedSchema(t *testing.T) {
	_, agyHooksJSON, _, _ := applyHooksForProfile(t, "base", map[string]string{
		"base": "name: base\nbundles:\n  - demo\n",
	})
	require.NotEmpty(t, agyHooksJSON, ".agents/hooks.json must be written")

	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(agyHooksJSON), &settings))

	var sawBundleHook bool
	for _, g := range settings.Hooks["SessionStart"] {
		require.NotEmpty(t, g.Hooks, "each group must carry a nested hooks[] array")
		for _, e := range g.Hooks {
			assert.Equal(t, "command", e.Type, "every Antigravity hook entry needs type:command")
			if strings.Contains(e.Command, "demo-hook") {
				sawBundleHook = true
			}
			assert.NotContains(t, e.Command, "session-bind",
				"session-bind must not register under SessionStart on agy (it would never fire)")
		}
	}
	assert.True(t, sawBundleHook, "bundle session_start hook must reach .agents/hooks.json in nested form")

	var sawSessionBind bool
	for _, g := range settings.Hooks["PreToolUse"] {
		for _, e := range g.Hooks {
			if strings.Contains(e.Command, "session-bind") {
				sawSessionBind = true
				assert.Equal(t, ".*", g.Matcher, "the diverted bind fires on every tool (first one binds)")
			}
		}
	}
	assert.True(t, sawSessionBind, "built-in `hook session-bind` must divert to PreToolUse on agy (pre_tool_fallback)")
}
