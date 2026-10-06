// Unit-level proofs that gateProfileMCP/gateProfileHooks key the executable
// trust gate off a directory profile's SOURCE ref
// (profiles.ResolvedProfile.SourceRef via profileGateRefFor), never its
// display name, and compose a gate ref with exactly one '#'. See
// dir_profile_test.go for the production end-to-end path of a genuinely local
// directory profile, and TestAssemble_LocalBundleShippedProfile_GateRefParsesAndAllows
// below for the single-'#' rule proven through PRODUCTION bundle-profile
// seeding (config.loadBundleProfileSeed), not a hand-built fixture.
package managedhooks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/profiles"
	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

func TestProfileGateRefFor_BundleShippedUsesSourceRef(t *testing.T) {
	resolved := &profiles.ResolvedProfile{SourceRef: "https://github.com/acme/tools@bundles/kit"}
	ref := profileGateRefFor(nil, resolved, "https://github.com/acme/tools@bundles/kit#profiles/dev")
	assert.Equal(t, "https://github.com/acme/tools@bundles/kit", ref.Base)
	// No cfg, so no loader to resolve the ORIGIN bundle's read: the posture is
	// UNCLAIMED, which every Authorizer withholds — the fail-closed direction
	// for a read nothing could establish.
	assert.False(t, ref.Read.Claimed(),
		"an origin bundle that cannot be read must leave the posture unclaimed, never assumed")
}

// A profile with no source has nothing to key the gate by: every resolved
// profile is a bundle's item, so this is a resolution that went wrong, and the
// read is left UNCLAIMED — withheld by every Authorizer — never minted as a
// project-local posture nothing verified.
func TestProfileGateRefFor_SourcelessProfileIsUnclaimed(t *testing.T) {
	ref := profileGateRefFor(nil, &profiles.ResolvedProfile{}, "my-local-profile")
	assert.Equal(t, "my-local-profile", ref.Base)
	assert.False(t, ref.Read.Claimed(), "a source-less profile's read is unclaimed: fail-closed")
}

func TestProfileGateRefFor_NilResolvedIsUnclaimed(t *testing.T) {
	ref := profileGateRefFor(nil, nil, "my-local-profile")
	assert.Equal(t, "my-local-profile", ref.Base)
	assert.False(t, ref.Read.Claimed())
}

// TestGateProfileHooks_RemoteSourcedProfile_GatedBySourceRef is the core
// payload-asserting proof: a REMOTE-sourced profile's directly-declared hook
// is WITHHELD from the produced hook set when the gate denies (untrusted key
// / unsigned) and REACHES it when the gate allows. The gate is consulted with
// the profile's non-local source ref and exactly one '#': a display name
// would read as local and be auto-allowed, and a second '#' would fail to
// parse and withhold the hook with no reviewable ref.
func TestGateProfileHooks_RemoteSourcedProfile_GatedBySourceRef(t *testing.T) {
	ref := profileGateRef{Base: "https://github.com/acme/tools@bundles/kit"}
	hooks := wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{
		{Command: "malicious-marker-command", Type: "command"},
	}}}

	var gotRefs []string
	denyGate := recordingAuthorizer(false, &gotRefs) // untrusted / unsigned: DENY
	out := gateProfileHooks(ref, hooks, denyGate)
	assert.Empty(t, out.Unified.PreTool, "a denied remote-sourced profile hook must be WITHHELD from the produced hook set")
	require.Len(t, gotRefs, 1)
	assert.Equal(t, "ctxloom+git://github.com/acme/tools//bundles/kit#hooks/pre_tool/0", gotRefs[0],
		"the gate must be consulted with the profile's SOURCE ref, not a bare display name — and with exactly one '#'")

	allowGate := testAuthorizer(true)
	out = gateProfileHooks(ref, hooks, allowGate)
	require.Len(t, out.Unified.PreTool, 1)
	assert.Equal(t, "malicious-marker-command", out.Unified.PreTool[0].Command,
		"an ALLOWED remote-sourced profile hook still reaches the produced set")
}

// TestGateProfileHooks_LocalProfile_StillFlowsThroughGate contrasts the
// remote case: a genuinely local profile's ref.Base is its bare display name
// (empty SourceRef, per profileGateRefFor) — its inline hook is gated through
// the exact same function, and the composed ref carries no '#bundles/' or URL
// at all, matching the honest-local bare-token shape parseSourceRef mints.
func TestGateProfileHooks_LocalProfile_StillFlowsThroughGate(t *testing.T) {
	ref := profileGateRef{Base: "my-local-profile"}
	hooks := wire.HooksConfig{Unified: wire.UnifiedHooks{PreTool: []wire.Hook{
		{Command: "local-hook-command", Type: "command"},
	}}}
	var gotRefs []string
	gate := recordingAuthorizer(true, &gotRefs)
	out := gateProfileHooks(ref, hooks, gate)
	require.Len(t, out.Unified.PreTool, 1)
	require.Len(t, gotRefs, 1)
	assert.Equal(t, "ctxloom+local:my-local-profile#hooks/pre_tool/0", gotRefs[0])
}

// TestAssemble_LocalBundleShippedProfile_GateRefParsesAndAllows is the
// PRODUCTION end-to-end test of the single-'#' gate ref: a local bundle ships
// a profile (via config.loadBundleProfileSeed — the exact machinery that
// seeds a bundle-shipped profile in production, local or remote) carrying an
// inline hook, and the default agent's profile IS that bundle-shipped
// profile's "<bundle>#profiles/<name>" ref (the directory-profile fallback
// branch in Assemble, NOT an inline
// config.yaml profile).
//
// The ref must be "<SourceRef>#hooks/pre_tool/0": single '#', so it parses,
// and — because this is a LOCAL bundle — it resolves IsLocal:true, so a
// permissive gate lets it through exactly like any other locally-authored
// content. A ref composed onto the profile ref
// ("<bundle>#profiles/<name>#hooks/pre_tool/0") would be cut at the FIRST '#'
// by the selector split, mis-parse as kind "profiles" (rejected), and withhold
// the hook PERMANENTLY with no valid ref to review.
func TestAssemble_LocalBundleShippedProfile_GateRefParsesAndAllows(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	bundletree.WriteOS(t, bundlesDir, "kit", ""+
		"version: \"1.0\"\n"+
		"profiles:\n  dev:\n    hooks:\n      unified:\n        pre_tool:\n          - command: bundle-shipped-hook\n            type: command\n")

	profileRef := remote.LocalBundleRef("kit") + refuri.ProfileSelector + "dev"
	cfg := gatedFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{profileRef}}},
		AppPaths:     []string{appDir},
	})

	// A permissive gate: proves the ref PARSES and reaches a decision at all
	// (a double-'#' ref fails at the selector parser inside the gate itself,
	// never reaching a caller-supplied gate function to ask).
	var gotRefs []string
	cfg.BindTrustForTesting(recordingTrust(&gotRefs))

	assembled := Assemble(cfg, nil)
	// Reaching the authorizer AT ALL is the point: a double-'#' ref does not parse
	// (trust.ParseSelector rejects kind "profiles"), so bundles.Decide withholds
	// it before any authorizer is consulted and gotRefs would be empty.
	require.Len(t, gotRefs, 1)
	// The ref is reported by its parsed IDENTITY, which for local content is the
	// bare bundle name: "ctxloom:local@bundles/kit" and "kit" are the same
	// identity by construction (trust.Ref.CanonicalURL maps both onto
	// remote.LocalSource), and the qualified spelling carries no extra fact.
	assert.Equal(t, "ctxloom+local:kit#hooks/pre_tool/0", gotRefs[0],
		"the composed ref must carry exactly one '#' and resolve to the local bundle")
	require.Len(t, assembled.Wire().Unified.PreTool, 1, "an ALLOWED bundle-shipped profile hook must reach the managed set")
	assert.Equal(t, "bundle-shipped-hook", assembled.Wire().Unified.PreTool[0].Command)
}

// TestAssemble_LocalBundleShippedProfile_DeniedIsWithheld is the
// payload-asserting deny-side twin: the SAME bundle-shipped profile hook,
// denied by the gate, must be ABSENT from the produced settings — not merely
// "an error occurred" — a withhold that only reports would be a silent no-op.
func TestAssemble_LocalBundleShippedProfile_DeniedIsWithheld(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)
	bundlesDir := paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	bundletree.WriteOS(t, bundlesDir, "kit", ""+
		"version: \"1.0\"\n"+
		"profiles:\n  dev:\n    hooks:\n      unified:\n        pre_tool:\n          - command: bundle-shipped-hook\n            type: command\n")

	profileRef := remote.LocalBundleRef("kit") + refuri.ProfileSelector + "dev"
	cfg := gatedFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{profileRef}}},
		AppPaths:     []string{appDir},
	})
	cfg.BindTrustForTesting(rejectingAll())

	assembled := Assemble(cfg, nil)
	assert.Empty(t, assembled.Wire().Unified.PreTool, "a denied bundle-shipped profile hook must be withheld from the produced settings, not merely fail silently in a way that still ships it")
}

// TestGateProfileHooks_EveryUnifiedEventSurvivesAnAllowingGate closes a gap the
// other tests in this file cannot see: gateProfileHooks REBUILDS the unified
// set from a hand-written literal, one `keep(...)` line per event. An event
// missing from that literal is not a compile error and not a gate DENY — it is
// a hook the user declared, the gate approved, and the writer never receives.
// It looks exactly like fail-closed behaviour and is not.
//
// Reflection over wire.UnifiedHooks rather than a list of events, for the usual
// reason: the list is the thing that goes stale.
func TestGateProfileHooks_EveryUnifiedEventSurvivesAnAllowingGate(t *testing.T) {
	typ := reflect.TypeOf(wire.UnifiedHooks{})
	ref := profileGateRef{Base: "my-local-profile"}

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		event := strings.Split(field.Tag.Get("yaml"), ",")[0]
		t.Run(event, func(t *testing.T) {
			var u wire.UnifiedHooks
			cmd := "cmd-" + event
			reflect.ValueOf(&u).Elem().Field(i).Set(reflect.ValueOf([]wire.Hook{{Command: cmd, Type: "command"}}))

			var gotRefs []string
			out := gateProfileHooks(ref, wire.HooksConfig{Unified: u}, recordingAuthorizer(true, &gotRefs))

			got, _ := reflect.ValueOf(out.Unified).Field(i).Interface().([]wire.Hook)
			require.Lenf(t, got, 1,
				"an ALLOWED %s hook was dropped by gateProfileHooks — it is missing from the rebuild literal, and the loss reads as a trust denial", field.Name)
			assert.Equal(t, cmd, got[0].Command)

			require.Len(t, gotRefs, 1, "the gate must be consulted exactly once for the hook")
			assert.Equal(t, "ctxloom+local:my-local-profile#hooks/"+event+"/0", gotRefs[0],
				"the gate identity must name the hook's own event")
		})
	}
}
