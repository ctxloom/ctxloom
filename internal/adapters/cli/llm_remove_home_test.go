package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunLLMRemove_HomePreviewNamesTheHomeApply: `llm remove --home` exists,
// and its bare preview names the apply command for the same file — --yes AND
// --home — so running what it says removes what it reported.
func TestRunLLMRemove_HomePreviewNamesTheHomeApply(t *testing.T) {
	require.NotNil(t, llmRemoveCmd.Flags().Lookup("home"), "llm remove takes --home")

	home := t.TempDir()
	t.Setenv("HOME", home)
	agentProject(t, "schema_version: 7\n")
	require.NoError(t, runLLMWrite(t, false, append([]string{"--home"}, referenceLabelArgs...)...))

	llmHome, llmRemoveYes = true, false
	t.Cleanup(func() { llmHome, llmRemoveYes = false, false })
	cmd, out := textCmd()
	require.NoError(t, runLLMRemove(cmd, []string{"qwen"}))
	assert.Contains(t, out.String(), "ctxloom llm remove qwen --yes --home")
}
