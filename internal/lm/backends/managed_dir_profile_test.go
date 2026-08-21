// Directory-profile inline hooks/mcp parity tests verify that a directory profile
// (.ctxloom/profiles/<name>.yaml) carrying inline hooks:/mcp: declarations reaches
// the SAME managed-hooks/MCP resolution as an inline profile — and that, because a
// directory profile may be remote-sourced, its directly-declared executables pass
// the SAME per-item executable trust gate as bundle hooks/MCP (a withheld one is
// dropped). The directory path reaches AssembleManagedHooks / AssembleManagedMCP
// through the loader fallback (profiles.ResolvedProfile.Hooks/MCP), not the inline
// config map.
package backends

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
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
	profilesDir := paths.ProfilesPath(appDir)
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
	cfg.DisableCompanionProbe()
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

// TestAssembleManagedHooks_DirProfileInlineHooks_FlowAndGate is the hook twin: a
// directory profile's inline hooks: reach the managed hook set, and the exec gate
// withholds an un-granted one.
func TestAssembleManagedHooks_DirProfileInlineHooks_FlowAndGate(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody})
	assembled := AssembleManagedHooks(cfg, "/tmp", "", nil)
	cmds := preToolCommandSet(assembled.Wire().Unified)
	assert.Contains(t, cmds, "keep-hook", "directory profile inline hooks reach the managed set")
	assert.Contains(t, cmds, "drop-hook")

	cfg2 := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody})
	keepHash := bundles.HashPayload(hookExecPayload(wire.Hook{Command: "keep-hook", Type: "command"}))
	cfg2.SetExecutableTrustGate(hashAuthorizer(keepHash))
	gated := preToolCommandSet(AssembleManagedHooks(cfg2, "/tmp", "", nil).Wire().Unified)
	assert.Contains(t, gated, "keep-hook", "a granted directory-profile hook is applied")
	assert.NotContains(t, gated, "drop-hook", "an un-granted directory-profile hook is withheld by the exec gate")
}

// TestAssembleManagedHooks_DirProfileMergesWithInlineDefault is the hook twin of
// the merge-parity case: hooks union across an inline default and a directory
// default.
func TestAssembleManagedHooks_DirProfileMergesWithAnotherDefault(t *testing.T) {
	// Two DIRECTORY profiles: the merge this pins is across the selected
	// defaults, and with the inline arm retired both sides are files. Only the
	// second profile's provenance changed; the union is the claim.
	cfg := dirProfileCfg(t, []string{"otherP", "dirP"},
		map[string]string{
			"dirP":   "hooks:\n  unified:\n    pre_tool:\n      - command: dir-hook\n        type: command\n",
			"otherP": "hooks:\n  unified:\n    pre_tool:\n      - command: other-hook\n        type: command\n",
		})
	// BOTH hooks must be granted now. This is the behaviour change the inline
	// arm's retirement makes visible: an inline profile's hooks were
	// trusted-local and reached the set UNGATED, so this test used to authorize
	// only dir-hook and still see both. Every declared hook is gated today, so
	// a hook the gate does not grant is withheld regardless of which profile
	// declared it.
	grant := hashAuthorizer(
		bundles.HashPayload(hookExecPayload(wire.Hook{Command: "dir-hook", Type: "command"})),
		bundles.HashPayload(hookExecPayload(wire.Hook{Command: "other-hook", Type: "command"})),
	)
	cfg.SetExecutableTrustGate(grant)

	cmds := preToolCommandSet(AssembleManagedHooks(cfg, "/tmp", "", nil).Wire().Unified)
	assert.Contains(t, cmds, "other-hook", "the second default profile's granted hook is applied")
	assert.Contains(t, cmds, "dir-hook", "the first default profile's granted hook is applied")
}

// TestAssembleManagedHooks_DirProfileInheritsParentHooks proves a directory
// profile's inline hooks union across parent inheritance (the Hooks threading
// through profiles.resolveProfileRecursive + ResolvedProfile.Merge), reaching the
// managed set together — parent/default merge parity.
func TestAssembleManagedHooks_DirProfileInheritsParentHooks(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"child"}, map[string]string{
		"base":  "hooks:\n  unified:\n    pre_tool:\n      - command: base-hook\n        type: command\n",
		"child": "parents:\n  - base\nhooks:\n  unified:\n    pre_tool:\n      - command: child-hook\n        type: command\n",
	})

	cmds := preToolCommandSet(AssembleManagedHooks(cfg, "/tmp", "", nil).Wire().Unified)
	assert.Contains(t, cmds, "base-hook", "a directory profile inherits its parent's inline hooks")
	assert.Contains(t, cmds, "child-hook")
}

func TestAssembleManagedHooks_DeniedHookIsWarned(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody})
	keepHash := bundles.HashPayload(hookExecPayload(wire.Hook{Command: "keep-hook", Type: "command"}))
	cfg.SetExecutableTrustGate(hashAuthorizer(keepHash))

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	gated := preToolCommandSet(AssembleManagedHooks(cfg, "/tmp", "", nil).Wire().Unified)
	assert.NotContains(t, gated, "drop-hook", "the gate's deny decision is unchanged")
	assert.Contains(t, buf.String(), "drop-hook",
		"a denied hook must be warned by name, not silently dropped: got %q", buf.String())
}

// TestAssembleManagedDenyTools_DirProfile proves a directory profile's
// deny_tools reaches AssembleManagedDenyTools — the deny-tools mirror of
// TestAssembleManagedHooks_DirProfileInlineHooks_FlowAndGate. Deliberately no
// executable trust gate set (deny_tools is never gated), so this ALSO proves
// an ungated cfg does not accidentally withhold it.
func TestAssembleManagedDenyTools_DirProfile(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{
		"dir": "deny_tools:\n  - Task\n",
	})

	got := AssembleManagedDenyTools(cfg, nil)
	assert.Equal(t, []string{"Task"}, got)
}

// TestAssembleManagedDenyTools_MergesInlineAndDirAndDedupes is the deny-tools
// mirror of TestAssembleManagedMCP_DirProfileMergesWithInlineDefault: the
// union spans an inline default profile and a directory default profile, and
// a tool named by both collapses to one entry.
func TestAssembleManagedDenyTools_MergesAcrossDefaultsAndDedupes(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"otherP", "dirP"}, map[string]string{
		"dirP":   "deny_tools:\n  - Task\n  - WebFetch\n",
		"otherP": "deny_tools:\n  - WebFetch\n",
	})

	got := AssembleManagedDenyTools(cfg, nil)
	assert.ElementsMatch(t, []string{"Task", "WebFetch"}, got,
		"union across the selected defaults, deduped: WebFetch is named by both and appears once")
}

// TestAssembleManagedDenyTools_Empty proves a nil cfg and a profile set with
// no deny_tools both degrade to an empty result (never nil-panics, never
// fabricates a denial) — the silent-no-op guard for the empty-input case.
func TestAssembleManagedDenyTools_Empty(t *testing.T) {
	assert.Nil(t, AssembleManagedDenyTools(nil, nil))

	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": "description: plain\n"})
	assert.Empty(t, AssembleManagedDenyTools(cfg, nil))
}
