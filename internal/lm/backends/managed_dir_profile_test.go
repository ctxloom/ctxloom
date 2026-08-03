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
func dirProfileCfg(t *testing.T, defaults []string, dirProfiles map[string]string, inline map[string]config.Profile) *config.Config {
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
		Profiles:     config.ProfilesConfig{Definitions: inline},
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

const dirMCPBody = "mcp:\n  servers:\n    keep-srv:\n      command: keep-cmd\n    drop-srv:\n      command: drop-cmd\n"
const dirHookBody = "hooks:\n  unified:\n    pre_tool:\n      - command: keep-hook\n        type: command\n      - command: drop-hook\n        type: command\n"

// TestAssembleManagedMCP_DirProfileInlineServers_FlowAndGate proves a directory
// profile's inline mcp: servers reach the managed MCP set (the gap this change
// closes), and that the executable trust gate withholds an un-granted one: with no
// gate both flow (parity with an inline profile); with a gate granting only one
// server's executable-surface hash, the other is dropped.
func TestAssembleManagedMCP_DirProfileInlineServers_FlowAndGate(t *testing.T) {
	// No gate (management path): both directory-declared servers flow.
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirMCPBody}, nil)
	mcp := AssembleManagedMCP(cfg, nil)
	assert.Contains(t, mcp.Servers, "keep-srv", "directory profile inline MCP servers reach the managed set")
	assert.Contains(t, mcp.Servers, "drop-srv")

	// Gate granting ONLY keep-srv's executable-surface hash → drop-srv withheld.
	cfg2 := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirMCPBody}, nil)
	keepHash := bundles.HashPayload(mcpExecPayload(wire.MCPServer{Command: "keep-cmd"}))
	cfg2.SetExecutableTrustGate(func(_ref string, payload []byte, _form, _signer string) bool {
		return bundles.HashPayload(payload) == keepHash
	})
	gated := AssembleManagedMCP(cfg2, nil)
	assert.Contains(t, gated.Servers, "keep-srv", "a granted directory-profile MCP server is applied")
	assert.NotContains(t, gated.Servers, "drop-srv", "an un-granted directory-profile MCP server is withheld by the exec gate")
}

// TestAssembleManagedHooks_DirProfileInlineHooks_FlowAndGate is the hook twin: a
// directory profile's inline hooks: reach the managed hook set, and the exec gate
// withholds an un-granted one.
func TestAssembleManagedHooks_DirProfileInlineHooks_FlowAndGate(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody}, nil)
	assembled := AssembleManagedHooks(cfg, "/tmp", "", nil)
	cmds := preToolCommandSet(assembled.Wire().Unified)
	assert.Contains(t, cmds, "keep-hook", "directory profile inline hooks reach the managed set")
	assert.Contains(t, cmds, "drop-hook")

	cfg2 := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody}, nil)
	keepHash := bundles.HashPayload(hookExecPayload(wire.Hook{Command: "keep-hook", Type: "command"}))
	cfg2.SetExecutableTrustGate(func(_ref string, payload []byte, _form, _signer string) bool {
		return bundles.HashPayload(payload) == keepHash
	})
	gated := preToolCommandSet(AssembleManagedHooks(cfg2, "/tmp", "", nil).Wire().Unified)
	assert.Contains(t, gated, "keep-hook", "a granted directory-profile hook is applied")
	assert.NotContains(t, gated, "drop-hook", "an un-granted directory-profile hook is withheld by the exec gate")
}

// TestAssembleManagedMCP_DirProfileMergesWithInlineDefault proves the managed MCP
// set unions across a DIRECTORY default profile and an INLINE default profile —
// the two resolution paths fold into one set, the headline parity this change
// brings (mirroring the prompt-curation directory+inline merge). The inline
// server is trusted-local (ungated); the directory server is gated and granted.
func TestAssembleManagedMCP_DirProfileMergesWithInlineDefault(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"inlineP", "dirP"},
		map[string]string{"dirP": "mcp:\n  servers:\n    dir-srv:\n      command: dir-cmd\n"},
		map[string]config.Profile{
			"inlineP": {MCP: wire.MCPConfig{Servers: map[string]wire.MCPServer{"inline-srv": {Command: "inline-cmd"}}}},
		})
	dirHash := bundles.HashPayload(mcpExecPayload(wire.MCPServer{Command: "dir-cmd"}))
	cfg.SetExecutableTrustGate(func(_ref string, payload []byte, _form, _signer string) bool {
		return bundles.HashPayload(payload) == dirHash
	})

	mcp := AssembleManagedMCP(cfg, nil)
	assert.Contains(t, mcp.Servers, "inline-srv", "inline default profile's MCP server (trusted-local) is applied")
	assert.Contains(t, mcp.Servers, "dir-srv", "directory default profile's granted MCP server is applied")
}

// TestAssembleManagedHooks_DirProfileMergesWithInlineDefault is the hook twin of
// the merge-parity case: hooks union across an inline default and a directory
// default.
func TestAssembleManagedHooks_DirProfileMergesWithInlineDefault(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"inlineP", "dirP"},
		map[string]string{"dirP": "hooks:\n  unified:\n    pre_tool:\n      - command: dir-hook\n        type: command\n"},
		map[string]config.Profile{
			"inlineP": {Hooks: wire.HooksConfig{Unified: wire.UnifiedHooks{
				PreTool: []wire.Hook{{Command: "inline-hook", Type: "command"}},
			}}},
		})
	dirHash := bundles.HashPayload(hookExecPayload(wire.Hook{Command: "dir-hook", Type: "command"}))
	cfg.SetExecutableTrustGate(func(_ref string, payload []byte, _form, _signer string) bool {
		return bundles.HashPayload(payload) == dirHash
	})

	cmds := preToolCommandSet(AssembleManagedHooks(cfg, "/tmp", "", nil).Wire().Unified)
	assert.Contains(t, cmds, "inline-hook", "inline default profile's hook (trusted-local) is applied")
	assert.Contains(t, cmds, "dir-hook", "directory default profile's granted hook is applied")
}

// TestAssembleManagedHooks_DirProfileInheritsParentHooks proves a directory
// profile's inline hooks union across parent inheritance (the Hooks threading
// through profiles.resolveProfileRecursive + ResolvedProfile.Merge), reaching the
// managed set together — parent/default merge parity.
func TestAssembleManagedHooks_DirProfileInheritsParentHooks(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"child"}, map[string]string{
		"base":  "hooks:\n  unified:\n    pre_tool:\n      - command: base-hook\n        type: command\n",
		"child": "parents:\n  - base\nhooks:\n  unified:\n    pre_tool:\n      - command: child-hook\n        type: command\n",
	}, nil)

	cmds := preToolCommandSet(AssembleManagedHooks(cfg, "/tmp", "", nil).Wire().Unified)
	assert.Contains(t, cmds, "base-hook", "a directory profile inherits its parent's inline hooks")
	assert.Contains(t, cmds, "child-hook")
}

// TestAssembleManagedMCP_DirProfileHonorsExcludeMCP proves a directory profile's
// exclude_mcp drops its own declared MCP server, parity with the inline
// profileBuilder.toProfile filter. No gate so both would otherwise flow.
func TestAssembleManagedMCP_DirProfileHonorsExcludeMCP(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{
		"dir": "mcp:\n  servers:\n    keep-srv:\n      command: keep-cmd\n    drop-srv:\n      command: drop-cmd\nexclude_mcp:\n  - drop-srv\n",
	}, nil)

	mcp := AssembleManagedMCP(cfg, nil)
	assert.Contains(t, mcp.Servers, "keep-srv")
	assert.NotContains(t, mcp.Servers, "drop-srv", "exclude_mcp drops the directory profile's own declared server")
}

// A profile MCP server / hook the executable trust gate DENIES must be
// diagnosable — a warning naming the withheld ref, not a silent drop. The
// gate's allow/deny decision itself (keep-hash matches keep-srv only) is
// unchanged from TestAssembleManagedMCP_DirProfileInlineServers_FlowAndGate.
func TestAssembleManagedMCP_DeniedServerIsWarned(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirMCPBody}, nil)
	keepHash := bundles.HashPayload(mcpExecPayload(wire.MCPServer{Command: "keep-cmd"}))
	cfg.SetExecutableTrustGate(func(_ref string, payload []byte, _form, _signer string) bool {
		return bundles.HashPayload(payload) == keepHash
	})

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	gated := AssembleManagedMCP(cfg, nil)
	assert.NotContains(t, gated.Servers, "drop-srv", "the gate's deny decision is unchanged")
	assert.Contains(t, buf.String(), "drop-srv",
		"a denied MCP server must be warned by name, not silently dropped: got %q", buf.String())
}

func TestAssembleManagedHooks_DeniedHookIsWarned(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": dirHookBody}, nil)
	keepHash := bundles.HashPayload(hookExecPayload(wire.Hook{Command: "keep-hook", Type: "command"}))
	cfg.SetExecutableTrustGate(func(_ref string, payload []byte, _form, _signer string) bool {
		return bundles.HashPayload(payload) == keepHash
	})

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
// TestAssembleManagedMCP_DirProfileInlineServers_FlowAndGate. Deliberately no
// executable trust gate set (deny_tools is never gated), so this ALSO proves
// an ungated cfg does not accidentally withhold it.
func TestAssembleManagedDenyTools_DirProfile(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{
		"dir": "deny_tools:\n  - Task\n",
	}, nil)

	got := AssembleManagedDenyTools(cfg, nil)
	assert.Equal(t, []string{"Task"}, got)
}

// TestAssembleManagedDenyTools_MergesInlineAndDirAndDedupes is the deny-tools
// mirror of TestAssembleManagedMCP_DirProfileMergesWithInlineDefault: the
// union spans an inline default profile and a directory default profile, and
// a tool named by both collapses to one entry.
func TestAssembleManagedDenyTools_MergesInlineAndDirAndDedupes(t *testing.T) {
	cfg := dirProfileCfg(t, []string{"inlineP", "dirP"},
		map[string]string{"dirP": "deny_tools:\n  - Task\n  - WebFetch\n"},
		map[string]config.Profile{
			"inlineP": {DenyTools: []string{"WebFetch"}},
		})

	got := AssembleManagedDenyTools(cfg, nil)
	assert.ElementsMatch(t, []string{"Task", "WebFetch"}, got, "union across inline+directory defaults, deduped")
}

// TestAssembleManagedDenyTools_Empty proves a nil cfg and a profile set with
// no deny_tools both degrade to an empty result (never nil-panics, never
// fabricates a denial) — the silent-no-op guard for the empty-input case.
func TestAssembleManagedDenyTools_Empty(t *testing.T) {
	assert.Nil(t, AssembleManagedDenyTools(nil, nil))

	cfg := dirProfileCfg(t, []string{"dir"}, map[string]string{"dir": "description: plain\n"}, nil)
	assert.Empty(t, AssembleManagedDenyTools(cfg, nil))
}
