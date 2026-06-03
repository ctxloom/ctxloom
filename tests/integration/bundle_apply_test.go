//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// against a fresh project dir, and returns the three written settings files.
func applyHooksForProfile(t *testing.T, defaultProfile string, profiles map[string]string) (mcpJSON, geminiJSON, claudeJSON string) {
	t.Helper()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := filepath.Join(appDir, "cache", "bundles") // paths.BundlesPath layout
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "demo.yaml"), []byte(applyDemoBundleYAML), 0o644))
	for name, content := range profiles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(content), 0o644))
	}

	projectDir := t.TempDir()
	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Defaults: []string{defaultProfile}},
		AppPaths: []string{appDir},
	}

	_, err := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:      "all",
		WorkDir:      projectDir,
		FS:           afero.NewOsFs(),
		ConfigLoader: func() (*config.Config, error) { return cfg, nil },
		ExecPath:     "ctxloom",
	})
	require.NoError(t, err)

	return readOrEmpty(t, filepath.Join(projectDir, ".mcp.json")),
		readOrEmpty(t, filepath.Join(projectDir, ".gemini", "settings.json")),
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
// (.mcp.json) and Gemini settings, and its hook reached the Claude settings.
func assertBundleApplied(t *testing.T, mcpJSON, geminiJSON, claudeJSON string) {
	t.Helper()
	assert.Contains(t, mcpJSON, "demo-server", "MCP server must land in .mcp.json")
	assert.Contains(t, mcpJSON, "demo-mcp", "MCP server command must land in .mcp.json")
	assert.Contains(t, geminiJSON, "demo-server", "MCP server must land in .gemini/settings.json")
	assert.Contains(t, claudeJSON, "demo-hook", "bundle hook must land in .claude/settings.json")
}

// TestBundleApply_DirectProfile: the demo bundle is referenced directly by the
// default profile. Baseline that MCP + hooks flow through apply.
func TestBundleApply_DirectProfile(t *testing.T) {
	mcpJSON, geminiJSON, claudeJSON := applyHooksForProfile(t, "base", map[string]string{
		"base": "name: base\nbundles:\n  - demo\n",
	})
	assertBundleApplied(t, mcpJSON, geminiJSON, claudeJSON)
}

// TestBundleApply_InheritedProfile: the demo bundle is referenced only by a
// parent profile; the default ("child") merely inherits it. This is the
// spotty-unstirred-anatomist regression driven end-to-end through ApplyHooks:
// before the fix, inherited bundles' MCP servers and hooks were silently dropped.
func TestBundleApply_InheritedProfile(t *testing.T) {
	mcpJSON, geminiJSON, claudeJSON := applyHooksForProfile(t, "child", map[string]string{
		"parent": "name: parent\nbundles:\n  - demo\n",
		"child":  "name: child\nparents:\n  - parent\n",
	})
	assertBundleApplied(t, mcpJSON, geminiJSON, claudeJSON)
}

// TestBundleApply_GeminiHookNestedSchema drives ApplyHooks end-to-end and asserts
// the bundle's session_start hook reaches .gemini/settings.json in Gemini's
// required nested shape (event → [{hooks:[{type:"command", command}]}]). A flat
// {command} object — the shape ctxloom emitted before — is silently ignored by
// Gemini, so this is the contract that makes Gemini hooks actually fire. The
// built-in `session bind` hook (the recovery producer) must also be present.
func TestBundleApply_GeminiHookNestedSchema(t *testing.T) {
	_, geminiJSON, _ := applyHooksForProfile(t, "base", map[string]string{
		"base": "name: base\nbundles:\n  - demo\n",
	})
	require.NotEmpty(t, geminiJSON, ".gemini/settings.json must be written")

	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(geminiJSON), &settings))

	groups := settings.Hooks["SessionStart"]
	require.NotEmpty(t, groups, "SessionStart must have hook groups in nested form")

	var sawBundleHook, sawSessionBind bool
	for _, g := range groups {
		require.NotEmpty(t, g.Hooks, "each group must carry a nested hooks[] array")
		for _, e := range g.Hooks {
			assert.Equal(t, "command", e.Type, "every Gemini hook entry needs type:command")
			if strings.Contains(e.Command, "demo-hook") {
				sawBundleHook = true
			}
			if strings.Contains(e.Command, "session bind") {
				sawSessionBind = true
			}
		}
	}
	assert.True(t, sawBundleHook, "bundle session_start hook must reach .gemini in nested form")
	assert.True(t, sawSessionBind, "built-in `session bind` (recovery producer) must reach .gemini")
}
