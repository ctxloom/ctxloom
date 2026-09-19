package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
)

// The bundle EXECUTABLE extractors gate conditionally: they build an
// executable-surface preimage only when something will judge it. That branch is
// where "deliberately ungated" and "the gate was forgotten" have to stay apart —
// skipping it admits an arbitrary command into backend settings unevaluated.
// bundles.AdmitAll skips it; a nil authorizer must not.

func sentinelExecBundle() *bundles.Bundle {
	return &bundles.Bundle{
		MCP: map[string]bundles.BundleMCP{"srv": {Command: "rm -rf /"}},
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{{Command: "rm -rf /", Type: "command"}},
		},
	}
}

// TestExtractMCP_ForgottenGate_WithholdsTheServer proves a nil authorizer does
// not reach settings: the server is omitted, not admitted unevaluated.
func TestExtractMCP_ForgottenGate_WithholdsTheServer(t *testing.T) {
	read := bundles.ProjectAuthoredRead("fixture", sentinelExecBundle())

	got := extractMCPFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), nil)
	assert.Empty(t, got, "a bundle MCP server reached settings with nothing having decided about it")

	ungated := extractMCPFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), composite.Ungated().Authorizer())
	require.Contains(t, ungated, "srv", "Ungated must admit exactly as the old nil did")
}

// TestExtractHooks_ForgottenGate_WithholdsTheHook is the hook half of the same
// contract.
func TestExtractHooks_ForgottenGate_WithholdsTheHook(t *testing.T) {
	read := bundles.ProjectAuthoredRead("fixture", sentinelExecBundle())

	got := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), nil, bundles.LinksUnchecked())
	assert.Empty(t, got.PreTool, "a bundle hook reached settings with nothing having decided about it")

	ungated := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), composite.Ungated().Authorizer(), bundles.LinksUnchecked())
	require.Len(t, ungated.PreTool, 1, "Ungated must admit exactly as the old nil did")
}

// TestExecutableTrustGate_UnboundConfigHoldsNoGate pins the accessor's
// fail-closed half: a Config no Owner published holds no gate, so the nil
// it reports is withheld on downstream (bundles.Decide), never admitted. A
// listing that means "ungated" binds composite.Ungated() by name, and a
// bound Trust decides.
func TestExecutableTrustGate_UnboundConfigHoldsNoGate(t *testing.T) {
	cfg := &Config{}

	require.Nil(t, cfg.ExecutableTrustGate(), "an unbound config must not report a gate it does not hold")

	cfg.BindTrustForTesting(composite.Ungated())
	assert.False(t, bundles.Gates(cfg.ExecutableTrustGate()), "a listing's Ungated() is the one ungated authorizer")
	assert.True(t, cfg.ExecutableTrustGate().Admit(bundles.Exposure{}).Allow, "the ungated authorizer must admit")

	cfg.BindTrustForTesting(compositetest.Trust(compositetest.RejectAll()))
	assert.True(t, bundles.Gates(cfg.ExecutableTrustGate()), "a bound Trust must decide")
}
