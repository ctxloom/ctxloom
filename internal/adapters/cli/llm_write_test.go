// Tests for llm_write.go: `llm create`/`llm edit`'s CRUD parity with
// `agent create`/`agent edit` (agent_test.go's TestCheckAgentExistence_*/
// TestBuildSetAgentRequest_*/TestRenderAgentWritten_* are the templates
// these mirror).
package cli

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

func TestCheckLLMExistence_EachVerbRefusesTheOthersCase(t *testing.T) {
	agentProject(t, "version: 6\nllm:\n  configs:\n    big: { type: codex }\n")
	cfg, err := GetConfig()
	require.NoError(t, err)

	assert.NoError(t, checkLLMExistence(cfg, "brand-new", false), "create accepts an unused label")
	assert.NoError(t, checkLLMExistence(cfg, "big", true), "edit accepts an existing label")

	err = checkLLMExistence(cfg, "big", false)
	require.Error(t, err, "create must refuse a label that already exists")
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "ctxloom llm edit big")

	err = checkLLMExistence(cfg, "nope", true)
	require.Error(t, err, "edit must refuse a label nothing defines")
	assert.Contains(t, err.Error(), "no llm named")
	assert.Contains(t, err.Error(), "ctxloom llm create nope")
}

// TestCheckLLMExistence_BareBackendNameCountsAsExisting proves `llm create`
// refuses a bare registered backend name (e.g. "claude-code") even with no
// config.yaml entry — `agent create finder --engine claude-code` already
// depends on that name resolving, and creating a SEPARATE, confusing
// same-named config entry via `llm create claude-code` would shadow it.
// `llm edit claude-code` is the sanctioned way to turn a built-in into an
// explicit config entry.
func TestCheckLLMExistence_BareBackendNameCountsAsExisting(t *testing.T) {
	agentProject(t, "version: 6\n")
	cfg, err := GetConfig()
	require.NoError(t, err)

	err = checkLLMExistence(cfg, "claude-code", false)
	require.Error(t, err, "create must refuse a name a registered backend already claims")

	assert.NoError(t, checkLLMExistence(cfg, "claude-code", true), "edit may still upgrade a built-in into an explicit entry")
}

func TestBuildSetLLMRequest_OnlySendsChangedFlags(t *testing.T) {
	cmd := &cobra.Command{}
	registerLLMWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse([]string{"--type", "mock"}))

	req, err := buildSetLLMRequest(cmd, "big")
	require.NoError(t, err)
	assert.Equal(t, "big", req.Label)
	require.NotNil(t, req.Type, "the flag that WAS typed must be sent")
	assert.Equal(t, "mock", *req.Type)
	assert.Nil(t, req.Model, "an untyped flag must stay nil so SetLLM preserves it")
	assert.Nil(t, req.Permissions)
}

func TestBuildSetLLMRequest_ExplicitEmptyIsSentAsAClear(t *testing.T) {
	cmd := &cobra.Command{}
	registerLLMWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse([]string{"--model", ""}))

	req, err := buildSetLLMRequest(cmd, "big")
	require.NoError(t, err)
	require.NotNil(t, req.Model, `--model "" must be sent, not treated as unnamed`)
	assert.Equal(t, "", *req.Model)
}

func TestRenderLLMWritten_NamesWhichVerbRan(t *testing.T) {
	entry := &operations.LLMEntry{Label: "big", Type: "mock", Model: "o1", Permissions: "bypass"}

	var created bytes.Buffer
	require.NoError(t, renderLLMWritten(&created, entry, false))
	assert.Contains(t, created.String(), `Created llm "big"`)
	assert.Contains(t, created.String(), "mock")
	assert.Contains(t, created.String(), "o1")
	assert.Contains(t, created.String(), "bypass")

	var edited bytes.Buffer
	require.NoError(t, renderLLMWritten(&edited, entry, true))
	assert.Contains(t, edited.String(), `Updated llm "big"`)
}
