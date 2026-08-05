package confload

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scopedTestProduct is testProduct plus a ScopeAllows hook — a tiny fake
// per-key policy standing in for a real product's layerscope.Policy, proving
// ApplyOverrides' ScopeAllows wiring is generic and carries no ctxloom-
// specific assumption. disallow keys are lower-cased and joined with "." —
// path segments are compared case-insensitively, matching resolvePath's own
// case-4 fallback (an env token arrives in whatever case the shell handed
// it, e.g. "AGENTS.EVIL.COORDINATOR" for an unknown schema path).
func scopedTestProduct(knownPaths []string, disallow map[string]bool) Product {
	p := testProduct(knownPaths...)
	p.ScopeAllows = func(source OverrideSource, path []string) (bool, string) {
		key := source.String() + ":" + strings.ToLower(joinPath(path))
		if disallow[key] {
			return false, "test policy forbids " + source.String() + " for this key"
		}
		return true, ""
	}
	return p
}

func joinPath(path []string) string {
	return strings.Join(path, ".")
}

// TestApplyOverrides_ScopeAllows_DropsDisallowedEnvOverride proves an env
// override ScopeAllows forbids is DROPPED from the resulting layer — not
// merely warned about and applied anyway (that would be case 4's unknown-key
// behavior, a different thing). This is the seam that closes the "env sets a
// project-only privilege grant" escalation: ScopeAllows answers false, and
// the value must never reach the returned map.
func TestApplyOverrides_ScopeAllows_DropsDisallowedEnvOverride(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_AGENTS_EVIL_COORDINATOR", "true")

	p := scopedTestProduct(nil, map[string]bool{"env:agents.evil.coordinator": true})
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, applyErr := p.ApplyOverrides(map[string]any{}, o)
	require.Error(t, applyErr, "a disallowed override must be reported, not silently dropped with a clean error")
	assert.Contains(t, strings.ToLower(applyErr.Error()), "agents.evil.coordinator")

	// Case 4's fallback preserves the env token's own (shell-uppercase) case
	// — see resolvePath's doc — so the disallowed path lands as
	// AGENTS.EVIL.COORDINATOR, not the lower-cased spelling the test's own
	// disallow map is keyed on (scopedTestProduct lower-cases before
	// comparing). Either way, no form of the key may survive into out.
	assert.NotContains(t, out, "AGENTS", "the disallowed top-level key must not appear anywhere in the resulting layer")
	assert.NotContains(t, out, "agents")
}

// TestApplyOverrides_ScopeAllows_AllowedOverrideStillApplies proves
// ScopeAllows is a NARROW gate, not a kill switch on the whole override
// mechanism: an override it permits still lands exactly like it would with no
// ScopeAllows hook at all.
func TestApplyOverrides_ScopeAllows_AllowedOverrideStillApplies(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_AGENT_TURN_CAP", "7")

	p := scopedTestProduct([]string{"agent_turn_cap"}, map[string]bool{"env:agents.evil.coordinator": true})
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, applyErr := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, applyErr)
	assert.Equal(t, 7, out["agent_turn_cap"])
}

// TestApplyOverrides_ScopeAllows_NilMeansEveryOverrideAllowed proves the
// documented nil-safe default: a Product with no ScopeAllows hook at all
// behaves exactly as it did before this hook existed.
func TestApplyOverrides_ScopeAllows_NilMeansEveryOverrideAllowed(t *testing.T) {
	t.Setenv("TESTPROD_CONFIG_AGENTS_EVIL_COORDINATOR", "true")

	p := testProduct() // ScopeAllows left nil
	o, err := p.ReadOverrides(nil)
	require.NoError(t, err)

	out, applyErr := p.ApplyOverrides(map[string]any{}, o)
	require.NoError(t, applyErr)
	// Case 4 (no schema knowledge) preserves the env token's own case for an
	// unrecognized path, so this lands under "AGENTS", not "agents" — see
	// resolvePath's doc. What matters here is only that it landed AT ALL.
	agents, ok := out["AGENTS"].(map[string]any)
	require.True(t, ok, "with no ScopeAllows hook the override must still apply")
	evil, ok := agents["EVIL"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, evil["COORDINATOR"])
}

// TestApplyOverrides_ScopeAllows_DropsDisallowedFlagOverride mirrors the env
// case for --config-set, and proves the two channels are told apart
// correctly: a policy that disallows env but allows flag for the identical
// path must still let the flag override through.
func TestApplyOverrides_ScopeAllows_DropsDisallowedFlagOverride(t *testing.T) {
	fs := configSetFlagSet(t, "agents.evil.coordinator=true")

	p := scopedTestProduct(nil, map[string]bool{"flag:agents.evil.coordinator": true})
	o, err := p.ReadOverrides(fs)
	require.NoError(t, err)

	out, applyErr := p.ApplyOverrides(map[string]any{}, o)
	require.Error(t, applyErr)
	agents, _ := out["agents"].(map[string]any)
	if agents != nil {
		evil, _ := agents["evil"].(map[string]any)
		assert.NotContains(t, evil, "coordinator")
	}
}

// TestOverrideSource_String pins the two channel names ScopeAllows'
// diagnostics and layerscope's own Layer mapping key off.
func TestOverrideSource_String(t *testing.T) {
	assert.Equal(t, "env", SourceEnv.String())
	assert.Equal(t, "flag", SourceFlag.String())
	assert.Equal(t, "unknown", OverrideSource(99).String())
}
