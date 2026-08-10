// Tests for llm_remove.go — mirrors agent_test.go's
// TestRunAgentRemove_BareReportsAndDestroysNothing/
// TestRunAgentRemove_YesRemovesAndReports pair.
package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

func TestRunLLMRemove_BareReportsAndDestroysNothing(t *testing.T) {
	agentProject(t, "version: 6\nllm:\n  configs:\n    big: { type: codex }\n")
	llmRemoveYes = false
	cmd, out := textCmd()
	require.NoError(t, runLLMRemove(cmd, []string{"big"}))
	assert.Contains(t, out.String(), "Nothing was removed")
	assert.Contains(t, out.String(), "--yes")

	config.Invalidate()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, ok := cfg.GetLLMEntry("big")
	assert.True(t, ok, "the bare (no --yes) path must leave the llm in config")
}

// TestRunLLMRemove_YesRemovesAndReports pins the apply side, paired with the
// bare-path test above so a regression in either direction is caught.
func TestRunLLMRemove_YesRemovesAndReports(t *testing.T) {
	agentProject(t, "version: 6\nllm:\n  configs:\n    big: { type: codex }\n")
	llmRemoveYes = true
	t.Cleanup(func() { llmRemoveYes = false })
	cmd, out := textCmd()
	require.NoError(t, runLLMRemove(cmd, []string{"big"}))
	assert.Contains(t, out.String(), `Removed llm "big"`)

	config.Invalidate()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, ok := cfg.GetLLMEntry("big")
	assert.False(t, ok, "--yes must actually remove the llm from config")
}

// TestRunLLMRemove_UnknownLabelErrors_EvenBare: the bare (report-only) path
// must still refuse a label nothing declares — a preview naming a target
// that was never really there would be worse than the not-found error.
// Mirrors the same guard `signer trust`/fragment/agent remove all share.
func TestRunLLMRemove_UnknownLabelErrors_EvenBare(t *testing.T) {
	agentProject(t, "version: 6\n")
	llmRemoveYes = false
	cmd, _ := textCmd()
	err := runLLMRemove(cmd, []string{"nope"})
	require.Error(t, err)
}

// TestRunLLMRemove_BareBackendNameIsNotRemovable proves a registered
// backend name with no config.yaml entry (e.g. "claude-code" on a project
// with no llm.configs at all — mergeDefaultConfig's whole-registry
// fallback merely fills the READ view, IsLLMUserAuthored sees through it)
// is refused, never falsely reported as removed.
func TestRunLLMRemove_BareBackendNameIsNotRemovable(t *testing.T) {
	agentProject(t, "version: 6\n")
	llmRemoveYes = true
	t.Cleanup(func() { llmRemoveYes = false })
	cmd, _ := textCmd()
	err := runLLMRemove(cmd, []string{"claude-code"})
	require.Error(t, err, "a never-configured built-in has nothing to remove")
}
