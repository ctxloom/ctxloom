//go:build integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bundle that ships both an MCP server and a hook. The bug class under test
// silently dropped a bundle's MCP server / hooks when the bundle was reachable
// only through profile inheritance.
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
// (Claude's .mcp.json and Claude's settings.json).
func applyHooksForProfile(t *testing.T, defaultProfile string, profiles map[string]string) (mcpJSON, claudeJSON string) {
	t.Helper()

	// The trust gate is consulted for every bundle item this apply resolves,
	// and it reads the USER countersignature store out of the real home.
	isolatedApprovals(t)
	isolatedRecords(t)

	// This helper runs IN-PROCESS against the developer's real PATH and real
	// home, and the `session-bind` hook these tests assert on ships in
	// TASKLOOM's loadout (cmd/taskloom/loadout.yaml), not in an embedded
	// bundle — so the assertions already depend on a real taskloom being
	// installed. Pin the exec-consent gate open so they do not ALSO depend on
	// whether this machine's ~/.ctxloom/companion_consent.yaml happens to have
	// approved it: the subject here is hook diversion, not admission, and
	// admission has its own tests.
	defer companions.AdmitEveryDiscoveredCompanionForTesting()()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := testenv.LocalBundlesDir(appDir)
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
	// The generation's gate: a Config reaching delivery without one is
	// refused at entry (config.ErrTrustUnbound). A project-local bundle and
	// the builtins are admitted by locality; nothing here travelled.
	cfg.BindTrustForTesting(compositetest.Trust())

	_, err := operations.ApplyHooks(context.Background(), operations.ApplyHooksRequest{
		// Empty, not "all": that selector was removed. An omitted backend now
		// means the project's CONFIGURED engines, and a named one must resolve.
		Backend: "",
		WorkDir: projectDir,
		FS:      afero.NewOsFs(),
		Cfg:     cfg,
	})
	require.NoError(t, err)

	return readOrEmpty(t, filepath.Join(projectDir, ".mcp.json")),
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
// (.mcp.json) store, and its hook reached the Claude settings.
func assertBundleApplied(t *testing.T, mcpJSON, claudeJSON string) {
	t.Helper()
	assert.Contains(t, mcpJSON, "demo-server", "MCP server must land in .mcp.json")
	assert.Contains(t, mcpJSON, "demo-mcp", "MCP server command must land in .mcp.json")
	assert.Contains(t, claudeJSON, "demo-hook", "bundle hook must land in .claude/settings.json")
}

// TestBundleApply_DirectProfile: the demo bundle is referenced directly by the
// default profile. Baseline that MCP + hooks flow through apply.
func TestBundleApply_DirectProfile(t *testing.T) {
	mcpJSON, claudeJSON := applyHooksForProfile(t, "base", map[string]string{
		"base": "name: base\nbundles:\n  - demo\n",
	})
	assertBundleApplied(t, mcpJSON, claudeJSON)
}

// TestBundleApply_InheritedProfile: the demo bundle is referenced only by a
// parent profile; the default ("child") merely inherits it. This is the
// inherited-bundle-drop regression driven end-to-end through ApplyHooks:
// before the fix, inherited bundles' MCP servers and hooks were silently dropped.
func TestBundleApply_InheritedProfile(t *testing.T) {
	mcpJSON, claudeJSON := applyHooksForProfile(t, "child", map[string]string{
		"parent": "name: parent\nbundles:\n  - demo\n",
		"child":  "name: child\nparents:\n  - parent\n",
	})
	assertBundleApplied(t, mcpJSON, claudeJSON)
}
