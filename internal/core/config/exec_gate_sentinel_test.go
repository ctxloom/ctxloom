package config

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/testsupport/admitall"
)

// The bundle EXECUTABLE extractors always consult the gate: every server and
// hook is decided through bundles.Decide. A forgotten (nil) gate must withhold
// rather than admit an arbitrary command into backend settings unevaluated.

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

	admitted := extractMCPFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), admitall.Authorizer())
	require.Contains(t, admitted, "srv", "control: a gate that admits delivers the server")
}

// TestExtractHooks_ForgottenGate_WithholdsTheHook is the hook half of the same
// contract.
func TestExtractHooks_ForgottenGate_WithholdsTheHook(t *testing.T) {
	read := bundles.ProjectAuthoredRead("fixture", sentinelExecBundle())

	got := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), nil, bundles.LinksUnchecked())
	assert.Empty(t, got.PreTool, "a bundle hook reached settings with nothing having decided about it")

	admitted := extractHooksFromBundle(report.Reporter{}, read, mustLocalRef(t, "src"), admitall.Authorizer(), bundles.LinksUnchecked())
	require.Len(t, admitted.PreTool, 1, "control: a gate that admits delivers the hook")
}

// TestExecutableTrustGate_UnboundConfigHoldsNoGate pins the accessor's
// fail-closed half: a Config no Owner published holds no gate, so the nil
// it reports is withheld on downstream (bundles.Decide), never admitted, and
// a bound Trust is the gate reported.
func TestExecutableTrustGate_UnboundConfigHoldsNoGate(t *testing.T) {
	cfg := &Config{}

	require.Nil(t, cfg.ExecutableTrustGate(), "an unbound config must not report a gate it does not hold")

	cfg.BindTrustForTesting(compositetest.Trust(compositetest.RejectAll()))
	require.NotNil(t, cfg.ExecutableTrustGate(), "a bound Trust must be the gate reported")
	assert.False(t, cfg.ExecutableTrustGate().Admit(bundles.Exposure{}).Allow, "the bound gate decides, and this one refuses")
}
