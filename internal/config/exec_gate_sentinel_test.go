package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
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

	got := extractMCPFromBundle(read, mustLocalRef(t, "src"), nil)
	assert.Empty(t, got, "a bundle MCP server reached settings with nothing having decided about it")

	ungated := extractMCPFromBundle(read, mustLocalRef(t, "src"), bundles.AdmitAll())
	require.Contains(t, ungated, "srv", "AdmitAll must admit exactly as the old nil did")
}

// TestExtractHooks_ForgottenGate_WithholdsTheHook is the hook half of the same
// contract.
func TestExtractHooks_ForgottenGate_WithholdsTheHook(t *testing.T) {
	read := bundles.ProjectAuthoredRead("fixture", sentinelExecBundle())

	got := extractHooksFromBundle(read, mustLocalRef(t, "src"), nil)
	assert.Empty(t, got.PreTool, "a bundle hook reached settings with nothing having decided about it")

	ungated := extractHooksFromBundle(read, mustLocalRef(t, "src"), bundles.AdmitAll())
	require.Len(t, ungated.PreTool, 1, "AdmitAll must admit exactly as the old nil did")
}

// TestExecutableTrustGate_UnsetConfigIsUngatedNotForgotten pins the accessor
// that keeps every management and listing path working: a config nobody
// attached a gate to reports the ungated authorizer, never a nil that would
// withhold everything downstream.
func TestExecutableTrustGate_UnsetConfigIsUngatedNotForgotten(t *testing.T) {
	cfg := &Config{}

	gate := cfg.ExecutableTrustGate()
	require.NotNil(t, gate, "an unset gate must not travel onward as nil")
	assert.False(t, bundles.Gates(gate), "an unset gate must be the ungated authorizer")
	assert.True(t, gate.Admit(bundles.Exposure{}).Allow, "the ungated authorizer must admit")

	cfg.SetExecutableTrustGate(testAuthorizer(false))
	assert.True(t, bundles.Gates(cfg.ExecutableTrustGate()), "an attached gate must decide")
}
