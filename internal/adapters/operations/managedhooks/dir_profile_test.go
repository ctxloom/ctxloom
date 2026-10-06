// Directory-profile inline hooks/mcp parity tests verify that a directory profile
// (.ctxloom/profiles/<name>.yaml) carrying inline hooks:/mcp: declarations reaches
// the SAME managed-hooks/MCP resolution as an inline profile — and that, because a
// directory profile may be remote-sourced, its directly-declared executables pass
// the SAME per-item executable trust gate as bundle hooks/MCP (a withheld one is
// dropped). The directory path reaches Assemble / Config.ResolveBundleMCPServersFor
// through the loader fallback (profiles.ResolvedProfile.Hooks/MCP), not the inline
// config map.
package managedhooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// dirProfileCfg writes directory profiles (name → YAML body) under a fresh
// tempdir's .ctxloom/profiles and returns a cfg whose AppPaths point at it, with
// the given default profiles. The profile loader reads the real filesystem (no fs
// is wired), matching how GetProfileDirs/os.Stat resolve directory profiles in
// production.
func dirProfileCfg(t *testing.T, defaults []string, dirProfiles map[string]string) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	for name, body := range dirProfiles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644))
	}
	cfg := config.NewFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: defaults}},
	})
	// Setting AppPaths arms companion probing, which execs the companion
	// binaries on the HOST's PATH — the fixture, not the machine, must decide
	// what these tests observe.
	return cfg
}

// preToolCommandSet returns the pre_tool hook commands in order.
func preToolCommandSet(h wire.UnifiedHooks) []string {
	cmds := make([]string, 0, len(h.PreTool))
	for _, hook := range h.PreTool {
		cmds = append(cmds, hook.Command)
	}
	return cmds
}

const dirHookBody = "hooks:\n  unified:\n    pre_tool:\n      - command: keep-hook\n        type: command\n      - command: drop-hook\n        type: command\n"

// TestAssemble_DirProfileInlineHooks_Flow is the hook twin: a directory
// profile's inline hooks reach the managed hook set.
func TestAssemble_DirProfileInlineHooks_Flow(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody})
	assembled := Assemble(cfg, nil)
	cmds := preToolCommandSet(assembled.Wire().Unified)
	assert.Contains(t, cmds, "keep-hook", "directory profile inline hooks reach the managed set")
	assert.Contains(t, cmds, "drop-hook")
}

// TestAssemble_DirProfileMergesWithAnotherDefault is the hook twin of
// the merge-parity case: hooks union across an inline default and a directory
// default.
func TestAssemble_DirProfileMergesWithAnotherDefault(t *testing.T) {
	// Two DIRECTORY profiles: the merge this pins is across the selected
	// defaults, and with the inline arm retired both sides are files. Only the
	// second profile's provenance changed; the union is the claim.
	cfg := dirProfileCfg(t, []string{"otherP", "dirP"},
		map[string]string{
			"dirP":   "hooks:\n  unified:\n    pre_tool:\n      - command: dir-hook\n        type: command\n",
			"otherP": "hooks:\n  unified:\n    pre_tool:\n      - command: other-hook\n        type: command\n",
		})
	cmds := preToolCommandSet(Assemble(cfg, nil).Wire().Unified)
	assert.Contains(t, cmds, "other-hook", "the second default profile's hook is applied")
	assert.Contains(t, cmds, "dir-hook", "the first default profile's hook is applied")
}

// TestAssemble_DirProfileInheritsParentHooks proves a directory
// profile's inline hooks union across parent inheritance (the Hooks threading
// through profiles.resolveProfileRecursive + ResolvedProfile.Merge), reaching the
// managed set together — parent/default merge parity.
func TestAssemble_DirProfileInheritsParentHooks(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"child"}, map[string]string{
		"base":  "hooks:\n  unified:\n    pre_tool:\n      - command: base-hook\n        type: command\n",
		"child": "parents:\n  - base\nhooks:\n  unified:\n    pre_tool:\n      - command: child-hook\n        type: command\n",
	})

	cmds := preToolCommandSet(Assemble(cfg, nil).Wire().Unified)
	assert.Contains(t, cmds, "base-hook", "a directory profile inherits its parent's inline hooks")
	assert.Contains(t, cmds, "child-hook")
}
