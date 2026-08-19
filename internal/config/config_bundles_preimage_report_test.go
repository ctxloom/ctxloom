package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestExtractHooksFromBundle_PreimageBuildFailure_IsReported pins U049-F17: a
// hook whose executable-surface preimage cannot be built is withheld
// (fail-closed, correct), but the failure was captured into perr and never
// reported — the configured hook vanished from the launched engine with no
// trace. The withhold must now record a fatal bundle-class finding naming the
// ref and the fault.
func TestExtractHooksFromBundle_PreimageBuildFailure_IsReported(t *testing.T) {
	resetStrictness(t)
	restore := SetPreimageBuildersForTesting(
		func(bundles.BundleHook) ([]byte, error) { return nil, errors.New("boom-hook") },
		nil,
	)
	defer restore()

	b := &bundles.Bundle{Name: "tools", Hooks: bundles.BundleHooks{
		PreTool: []bundles.BundleHook{{Command: "echo a", Type: "command"}},
	}}

	mark := strictness.Checkpoint()
	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", b), mustLocalRef(t, "remote/tools"), recordingGate(nil))

	assert.Empty(t, got.PreTool, "fail-closed: a hook whose preimage cannot be built is withheld")

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "a withheld hook must be reported, not silently dropped")
	assert.Equal(t, strictness.ClassBundle, findings[0].Class)
	assert.Contains(t, findings[0].Message, "remote/tools#hooks/pre_tool/0", "the finding names the withheld hook ref")
	assert.Contains(t, findings[0].Message, "boom-hook", "the finding surfaces the underlying fault")
}

// TestExtractMCPFromBundle_PreimageBuildFailure_IsReported is the MCP-server
// twin of the hook case above (U049-F17).
func TestExtractMCPFromBundle_PreimageBuildFailure_IsReported(t *testing.T) {
	resetStrictness(t)
	restore := SetPreimageBuildersForTesting(
		nil,
		func(bundles.BundleMCP) ([]byte, error) { return nil, errors.New("boom-mcp") },
	)
	defer restore()

	b := &bundles.Bundle{Name: "tools", MCP: map[string]bundles.BundleMCP{
		"alpha": {Command: "alpha-cmd"},
	}}

	mark := strictness.Checkpoint()
	got := extractMCPFromBundle(bundles.ProjectAuthoredRead("fixture", b), mustLocalRef(t, "remote/tools"), recordingGate(nil))

	assert.Empty(t, got, "fail-closed: an MCP server whose preimage cannot be built is withheld")

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "a withheld MCP server must be reported, not silently dropped")
	assert.Equal(t, strictness.ClassBundle, findings[0].Class)
	assert.Contains(t, findings[0].Message, "remote/tools#mcp/alpha", "the finding names the withheld server ref")
	assert.Contains(t, findings[0].Message, "boom-mcp", "the finding surfaces the underlying fault")
}
