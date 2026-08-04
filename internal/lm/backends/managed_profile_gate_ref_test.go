// Unit-level proofs for the ugly-sake/uncut-grub fix: gateProfileMCP/
// gateProfileHooks now key the executable trust gate off a directory
// profile's SOURCE ref (profiles.ResolvedProfile.SourceRef via
// profileGateRefFor), not its display name. See managed_dir_profile_test.go
// for the production end-to-end path (a genuinely local directory profile,
// unaffected by this fix) and TestAssembleManagedHooks_LocalBundleShippedProfile_UncutGrubFixed
// below for the uncut-grub double-'#' regression proven through PRODUCTION
// bundle-profile seeding (config.loadBundleProfileSeed), not a hand-built fixture.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/profiles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

func TestProfileGateRefFor_BundleShippedUsesSourceRef(t *testing.T) {
	resolved := &profiles.ResolvedProfile{SourceRef: "https://github.com/acme/tools@bundles/kit", Signer: "vendor@example.com"}
	ref := profileGateRefFor(resolved, "https://github.com/acme/tools@bundles/kit#profiles/dev")
	assert.Equal(t, "https://github.com/acme/tools@bundles/kit", ref.Base)
	assert.Equal(t, "vendor@example.com", ref.Signer)
}

func TestProfileGateRefFor_LocalFallsBackToProfileName(t *testing.T) {
	resolved := &profiles.ResolvedProfile{} // SourceRef empty: genuinely local
	ref := profileGateRefFor(resolved, "my-local-profile")
	assert.Equal(t, "my-local-profile", ref.Base)
	assert.Empty(t, ref.Signer)
}

func TestProfileGateRefFor_NilResolvedFallsBackToProfileName(t *testing.T) {
	ref := profileGateRefFor(nil, "my-local-profile")
	assert.Equal(t, "my-local-profile", ref.Base)
}

// TestGateProfileHooks_RemoteSourcedProfile_UglySakeFixed is the core
// payload-asserting proof: a REMOTE-sourced profile's directly-declared hook
// is WITHHELD from the produced hook set when the gate denies (untrusted key
// / unsigned) — the gate is consulted with a non-local, single-'#' ref
// (ugly-sake: no longer auto-allowed as local; uncut-grub: no longer a
// dead double-'#' withhold) — and REACHES the produced set when the gate
// allows.
func TestGateProfileHooks_RemoteSourcedProfile_UglySakeFixed(t *testing.T) {
	ref := profileGateRef{Base: "https://github.com/acme/tools@bundles/kit"}
	hooks := wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{
		{Command: "malicious-marker-command", Type: "command"},
	}}}

	var gotRefs []string
	denyGate := func(gotRef string, _ []byte, _, _ string) bool {
		gotRefs = append(gotRefs, gotRef)
		return false // untrusted / unsigned: DENY
	}
	out := gateProfileHooks(ref, hooks, denyGate)
	assert.Empty(t, out.Unified.PreTool, "a denied remote-sourced profile hook must be WITHHELD from the produced hook set")
	require.Len(t, gotRefs, 1)
	assert.Equal(t, "https://github.com/acme/tools@bundles/kit#hooks/pre_tool/0", gotRefs[0],
		"the gate must be consulted with the profile's SOURCE ref, not a bare display name — and with exactly one '#'")

	allowGate := testFilter(true)
	out = gateProfileHooks(ref, hooks, allowGate)
	require.Len(t, out.Unified.PreTool, 1)
	assert.Equal(t, "malicious-marker-command", out.Unified.PreTool[0].Command,
		"an ALLOWED remote-sourced profile hook still reaches the produced set")
}

func TestGateProfileMCP_RemoteSourcedProfile_UglySakeFixed(t *testing.T) {
	ref := profileGateRef{Base: "https://github.com/acme/tools@bundles/kit"}
	mcp := wire.MCPConfig{Servers: map[string]wire.MCPServer{
		"evil-server": {Command: "malicious-marker-command"},
	}}

	var gotRefs []string
	denyGate := func(gotRef string, _ []byte, _, _ string) bool {
		gotRefs = append(gotRefs, gotRef)
		return false
	}
	out := gateProfileMCP(ref, mcp, denyGate)
	assert.NotContains(t, out.Servers, "evil-server", "a denied remote-sourced profile MCP server must be WITHHELD")
	require.Len(t, gotRefs, 1)
	assert.Equal(t, "https://github.com/acme/tools@bundles/kit#mcp/evil-server", gotRefs[0])
}

// TestGateProfileHooks_LocalProfile_StillFlowsThroughGate contrasts the
// remote case: a genuinely local profile's ref.Base is its bare display name
// (empty SourceRef, per profileGateRefFor) — its inline hook is gated through
// the exact same function, and the composed ref carries no '#bundles/' or URL
// at all, matching trust.ParseItemRef's honest-local bare-token shape.
func TestGateProfileHooks_LocalProfile_StillFlowsThroughGate(t *testing.T) {
	ref := profileGateRef{Base: "my-local-profile"}
	hooks := wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{
		{Command: "local-hook-command", Type: "command"},
	}}}
	var gotRefs []string
	gate := func(gotRef string, _ []byte, _, _ string) bool {
		gotRefs = append(gotRefs, gotRef)
		return true
	}
	out := gateProfileHooks(ref, hooks, gate)
	require.Len(t, out.Unified.PreTool, 1)
	require.Len(t, gotRefs, 1)
	assert.Equal(t, "my-local-profile#hooks/pre_tool/0", gotRefs[0])
}

// TestAssembleManagedHooks_LocalBundleShippedProfile_UncutGrubFixed is the
// PRODUCTION end-to-end regression test for uncut-grub: a local bundle ships
// a profile (via config.loadBundleProfileSeed — the exact machinery that
// seeds a bundle-shipped profile in production, local or remote) carrying an
// inline hook, and the default agent's profile IS that bundle-shipped
// profile's "<bundle>#profiles/<name>" ref (the directory-profile fallback
// branch in AssembleManagedMCP/AssembleManagedHooks, NOT an inline
// config.yaml profile).
//
// Before the fix, the gate ref was built as
// "<bundle>#profiles/<name>#hooks/pre_tool/0" (double '#') — trust.ParseItemRef
// cuts at the FIRST '#', so this mis-parsed as kind "profiles" (rejected) and
// the hook was PERMANENTLY withheld with no valid ref to review. After the
// fix, the ref is "<SourceRef>#hooks/pre_tool/0" (single '#', parses, and —
// because this is a LOCAL bundle — resolves IsLocal:true), so a permissive
// gate lets it through exactly like any other locally-authored content.
func TestAssembleManagedHooks_LocalBundleShippedProfile_UncutGrubFixed(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "kit.yaml"), []byte(""+
		"name: kit\nversion: \"1.0\"\n"+
		"profiles:\n  dev:\n    hooks:\n      unified:\n        pre_tool:\n          - command: bundle-shipped-hook\n            type: command\n"),
		0o644))

	profileRef := remote.LocalBundleRef("kit") + remote.ProfileSelector + "dev"
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{profileRef}}},
		AppPaths:     []string{appDir},
	})
	cfg.DisableCompanionProbe()

	// A permissive gate: proves the ref PARSES and reaches a decision at all
	// (before the fix, the double-'#' ref failed to parse inside the gate
	// itself, at trust.ParseItemRef — never even reaching a
	// caller-supplied gate function to ask).
	var gotRefs []string
	cfg.SetExecutableTrustGate(func(ref string, _ []byte, _, _ string) bool {
		gotRefs = append(gotRefs, ref)
		return true
	})

	assembled := AssembleManagedHooks(cfg, "/tmp", "", nil)
	require.Len(t, gotRefs, 1)
	assert.Equal(t, remote.LocalBundleRef("kit")+"#hooks/pre_tool/0", gotRefs[0],
		"the composed ref must carry exactly one '#' (uncut-grub fixed)")
	require.Len(t, assembled.Wire().Unified.PreTool, 1, "an ALLOWED bundle-shipped profile hook must reach the managed set")
	assert.Equal(t, "bundle-shipped-hook", assembled.Wire().Unified.PreTool[0].Command)
}

// TestAssembleManagedHooks_LocalBundleShippedProfile_DeniedIsWithheld is the
// payload-asserting deny-side twin: the SAME bundle-shipped profile hook,
// denied by the gate, must be ABSENT from the produced settings — not merely
// "an error occurred" — the silent-no-op trap this whole fix exists to close.
func TestAssembleManagedHooks_LocalBundleShippedProfile_DeniedIsWithheld(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "kit.yaml"), []byte(""+
		"name: kit\nversion: \"1.0\"\n"+
		"profiles:\n  dev:\n    hooks:\n      unified:\n        pre_tool:\n          - command: bundle-shipped-hook\n            type: command\n"),
		0o644))

	profileRef := remote.LocalBundleRef("kit") + remote.ProfileSelector + "dev"
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{profileRef}}},
		AppPaths:     []string{appDir},
	})
	cfg.DisableCompanionProbe()
	cfg.SetExecutableTrustGate(testFilter(false))

	assembled := AssembleManagedHooks(cfg, "/tmp", "", nil)
	assert.Empty(t, assembled.Wire().Unified.PreTool, "a denied bundle-shipped profile hook must be withheld from the produced settings, not merely fail silently in a way that still ships it")
}
