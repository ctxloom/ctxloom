package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// recordingGate denies any ref containing one of denySubstrs and records every
// (ref → hash) it is fed, so a test can assert the executable choke passes the
// ref shape "<bundle>#mcp/<name>" / "<bundle>#hooks/<event>/<index>" and the
// item's ComputeContentHash. A nil seen map just decides.
func recordingGate(seen map[string]string, denySubstrs ...string) bundles.Filter {
	return bundles.FilterFunc(func(e bundles.Exposure) bundles.Verdict {
		ref := e.RefString()
		if seen != nil {
			seen[ref] = bundles.HashPayload(e.Bytes)
		}
		for _, s := range denySubstrs {
			if strings.Contains(ref, s) {
				return bundles.Verdict{Reason: bundles.ReasonPending}
			}
		}
		return bundles.Verdict{Admit: true, Reason: bundles.ReasonLocal}
	})
}

// localRead presents an already-parsed fixture bundle to the executable chokes
// as project content, which is what these tests are about: the choke's ref shape
// and preimage, not the posture.

// TestExtractMCPFromBundle_GateOmitsDeniedKeepsTrusted proves the MCP-server
// choke (TR5): a denied server is omitted from the resolved map while a trusted
// sibling survives, and the gate is fed the ref "<bundle>#mcp/<name>" with the
// server's executable-surface ComputeContentHash.
func TestExtractMCPFromBundle_GateOmitsDeniedKeepsTrusted(t *testing.T) {
	b := &bundles.Bundle{
		Name: "tools",
		MCP: map[string]bundles.BundleMCP{
			"alpha": {Command: "alpha-cmd", Args: []string{"--x"}},
			"beta":  {Command: "beta-cmd"},
		},
	}
	seen := map[string]string{}
	got := extractMCPFromBundle(bundles.ProjectAuthoredRead("fixture", b), "remote/tools", recordingGate(seen, "#mcp/beta"))

	require.Contains(t, got, "alpha", "trusted MCP server must survive the gate")
	require.NotContains(t, got, "beta", "denied MCP server must be omitted from settings")
	assert.Equal(t, "bundle:remote/tools", got["alpha"].SCM, "SCM marker still set on the kept server")

	// The gate is keyed on the bundle's SOURCE ref (matching the baseline + grant
	// key, and making the cascade's IsLocal/RepoURL honest), fed the
	// executable-surface hash of the exact server.
	alpha := b.MCP["alpha"]
	beta := b.MCP["beta"]
	assert.Equal(t, alpha.ComputeContentHash(), seen["remote/tools#mcp/alpha"])
	assert.Equal(t, beta.ComputeContentHash(), seen["remote/tools#mcp/beta"])
}

// TestExtractMCPFromBundle_FailClosed proves a gate that denies everything
// (e.g. an unreadable store) withholds every server — the fail-closed direction.
func TestExtractMCPFromBundle_FailClosed(t *testing.T) {
	b := &bundles.Bundle{Name: "tools", MCP: map[string]bundles.BundleMCP{
		"alpha": {Command: "a"}, "beta": {Command: "b"},
	}}
	denyAll := testFilter(false)
	got := extractMCPFromBundle(bundles.ProjectAuthoredRead("fixture", b), "remote/tools", denyAll)
	assert.Empty(t, got, "fail-closed: a deny-all gate withholds every MCP server")
}

// TestExtractMCPFromBundle_NilGate_Ungated proves builtin/management callers
// (nil gate) resolve every server unchanged — no enforcement.
func TestExtractMCPFromBundle_NilGate_Ungated(t *testing.T) {
	b := &bundles.Bundle{Name: "tools", MCP: map[string]bundles.BundleMCP{
		"alpha": {Command: "a"}, "beta": {Command: "b"},
	}}
	got := extractMCPFromBundle(bundles.ProjectAuthoredRead("fixture", b), "builtin:tools", nil)
	assert.Len(t, got, 2, "nil gate must not gate anything")
}

// TestExtractHooksFromBundle_GateOmitsDeniedKeepsTrusted proves the bundle-hook
// choke (TR5): a denied hook is omitted while siblings survive, keyed on the
// "<bundle>#hooks/<event>/<index>" identity with the hook's ComputeContentHash.
func TestExtractHooksFromBundle_GateOmitsDeniedKeepsTrusted(t *testing.T) {
	b := &bundles.Bundle{
		Name: "tools",
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{
				{Matcher: "Bash", Command: "echo a", Type: "command"},
				{Matcher: "Bash", Command: "echo b", Type: "command"},
			},
			PostFileEdit: []bundles.BundleHook{
				{Command: "echo c", Type: "command"},
			},
		},
	}
	seen := map[string]string{}
	// Deny the second pre_tool hook ("echo b", index 1).
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", b), "remote/tools", recordingGate(seen, "#hooks/pre_tool/1"))

	require.Len(t, got.PreTool, 1, "the denied pre_tool hook must be omitted")
	assert.Equal(t, "echo a", got.PreTool[0].Command, "the trusted sibling hook survives")
	assert.Equal(t, "bundle:remote/tools", got.PreTool[0].SCM)
	require.Len(t, got.PostFileEdit, 1, "an unrelated event's hook is unaffected")
	assert.Equal(t, "echo c", got.PostFileEdit[0].Command)

	// Identity scheme + hash: each hook is fed "<source>#hooks/<event>/<index>"
	// (source ref, not bundle.Name) with its executable-surface hash.
	h0 := b.Hooks.PreTool[0]
	h1 := b.Hooks.PreTool[1]
	hc := b.Hooks.PostFileEdit[0]
	assert.Equal(t, h0.ComputeContentHash(), seen["remote/tools#hooks/pre_tool/0"])
	assert.Equal(t, h1.ComputeContentHash(), seen["remote/tools#hooks/pre_tool/1"])
	assert.Equal(t, hc.ComputeContentHash(), seen["remote/tools#hooks/post_file_edit/0"])
}

// TestExtractHooksFromBundle_FailClosed proves a deny-all gate withholds every
// bundle hook (fail-closed).
func TestExtractHooksFromBundle_FailClosed(t *testing.T) {
	b := &bundles.Bundle{Name: "tools", Hooks: bundles.BundleHooks{
		PreTool:  []bundles.BundleHook{{Command: "echo a", Type: "command"}},
		PostTool: []bundles.BundleHook{{Command: "echo b", Type: "command"}},
	}}
	denyAll := testFilter(false)
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", b), "remote/tools", denyAll)
	assert.Empty(t, got.PreTool, "fail-closed: deny-all withholds pre_tool hooks")
	assert.Empty(t, got.PostTool, "fail-closed: deny-all withholds post_tool hooks")
}

// TestResolveBundleMCPServers_GatedEndToEnd drives the full profile→bundle→
// settings path with a field-injected gate: a denied profile-bundle MCP server
// is absent from the resolved set while a trusted one survives, and a
// COMPANION LOADOUT's MCP server (S8) is routed through the identical gate —
// never exempt like the old in-binary builtin path was before that gate
// threading fix — so it comes through only because this fake gate's
// denySubstrs doesn't happen to match it (see
// TestExecGate_ResolveBundleMCPServers_CompanionRejectable for the proof that
// a companion server CAN be specifically withheld, and
// TestResolveBundleMCPServers_IncludesCompanionLoadoutServers_Gated for the
// full allow/deny pair against this exact wiring).
func TestResolveBundleMCPServers_GatedEndToEnd(t *testing.T) {
	admitEveryDiscoveredCompanion(t)
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "ltk" {
			return "/fake/ltk", nil
		}
		return "", exec.ErrNotFound
	})
	defer restoreLook()
	envelope, err := signing.EncodeLoadoutEnvelope(
		[]byte("version: \"1.0.0\"\nmcp:\n  ltk-server:\n    command: ltk\n    args: [\"serve\"]\n"), nil, "")
	require.NoError(t, err)
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return envelope, nil })
	defer restoreProbe()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte("name: dev\nbundles:\n  - mcp-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "mcp-bundle.yaml"),
		[]byte("name: mcp-bundle\nversion: \"1.0\"\nmcp:\n  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n"), 0o644))

	cfg := &Config{defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, appPaths: []string{appDir}}
	cfg.SetExecutableTrustGate(recordingGate(nil, "#mcp/noisy-server"))

	result := cfg.ResolveBundleMCPServers(nil)
	assert.Contains(t, result, "quiet-server", "trusted profile-bundle MCP server must be written")
	assert.NotContains(t, result, "noisy-server", "denied profile-bundle MCP server must NOT be written")

	foundCompanion := false
	for _, srv := range result {
		if strings.HasPrefix(srv.SCM, "bundle:ctxloom:companion@") {
			foundCompanion = true
		}
	}
	assert.True(t, foundCompanion, "a companion loadout MCP server passes the SAME gate as a profile-bundle server — not specifically denied here, and never exempt")
}

// TestResolveBundleHooks_GatedEndToEnd drives the full path for hooks: a denied
// profile-bundle hook is absent while a trusted one survives.
func TestResolveBundleHooks_GatedEndToEnd(t *testing.T) {
	restore := SetLookPathForTesting(func(string) (string, error) { return "/usr/bin/x", nil })
	defer restore()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte("name: dev\nbundles:\n  - hook-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "hook-bundle.yaml"), []byte(hookBundleYAML), 0o644))

	cfg := &Config{defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}}, appPaths: []string{appDir}}
	// Deny only the session_start hook (index 0 of that event).
	cfg.SetExecutableTrustGate(recordingGate(nil, "#hooks/session_start/0"))

	result := cfg.ResolveBundleHooks(nil)
	assert.True(t, hasHookCommand(result.PreTool, "echo pre-tool", "bundle:hook-bundle"),
		"a trusted profile-bundle hook must survive")
	assert.False(t, hasHookCommand(result.SessionStart, "echo session-start", "bundle:hook-bundle"),
		"the denied profile-bundle hook must NOT be applied")
}
