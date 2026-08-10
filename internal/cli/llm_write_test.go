// Tests for llm_write.go: `llm create`/`llm edit`'s CRUD parity with
// `agent create`/`agent edit` (agent_test.go's TestCheckAgentExistence_*/
// TestBuildSetAgentRequest_*/TestRenderAgentWritten_* are the templates
// these mirror).
package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// strPtr is a local *string helper for building operations request literals
// in this file's tests (agent_write_test.go's operations-package ptr[T]
// helper is not visible from this package).
func strPtr(s string) *string { return &s }

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
	require.NoError(t, cmd.Flags().Parse([]string{"--type", "codex"}))

	req, err := buildSetLLMRequest(cmd, "big")
	require.NoError(t, err)
	assert.Equal(t, "big", req.Label)
	require.NotNil(t, req.Type, "the flag that WAS typed must be sent")
	assert.Equal(t, "codex", *req.Type)
	assert.Nil(t, req.Model, "an untyped flag must stay nil so SetLLM preserves it")
	assert.Nil(t, req.Permissions)
	assert.Nil(t, req.Env, "--env-file not passed must leave Env nil, never an empty map that would clear a stored one")
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
	entry := &operations.LLMEntry{Label: "big", Type: "codex", Model: "o1", Permissions: "bypass"}

	var created bytes.Buffer
	require.NoError(t, renderLLMWritten(&created, entry, false))
	assert.Contains(t, created.String(), `Created llm "big"`)
	assert.Contains(t, created.String(), "codex")
	assert.Contains(t, created.String(), "o1")
	assert.Contains(t, created.String(), "bypass")

	var edited bytes.Buffer
	require.NoError(t, renderLLMWritten(&edited, entry, true))
	assert.Contains(t, edited.String(), `Updated llm "big"`)
}

// TestRenderLLMWritten_ReportsEnvKeyPresence proves the write confirmation
// names WHICH env keys are declared — the withholding claim only means
// something if presence is actually surfaced somewhere.
func TestRenderLLMWritten_ReportsEnvKeyPresence(t *testing.T) {
	entry := &operations.LLMEntry{Label: "big", EnvKeys: []string{"OPENAI_API_KEY"}}
	var buf bytes.Buffer
	require.NoError(t, renderLLMWritten(&buf, entry, false))
	assert.Contains(t, buf.String(), "OPENAI_API_KEY")
}

// TestRenderLLMWritten_NeverEchoesASecretEnvValue is the credential-
// withholding mutation-killing test: a REAL secret goes all the way through
// SetLLM (real config.Manager, real save+reload) and the resulting
// operations.LLMEntry is rendered by the exact function `llm create`/`llm
// edit` uses for their success output. The secret VALUE must never appear
// anywhere in that text — only the key NAME (presence).
func TestRenderLLMWritten_NeverEchoesASecretEnvValue(t *testing.T) {
	agentProject(t, "version: 6\n")
	t.Setenv("HOME", t.TempDir())

	const secret = "sk-TOTALLY-SECRET-abc123-do-not-print-this"
	entry, err := operations.SetLLM(config.NewManager(), operations.SetLLMRequest{
		Label: "big",
		Type:  strPtr("codex"),
		Env:   map[string]string{"OPENAI_API_KEY": secret},
	})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, renderLLMWritten(&buf, entry, false))
	out := buf.String()
	assert.Contains(t, out, "OPENAI_API_KEY", "presence must be shown")
	assert.NotContains(t, out, secret, "a credential VALUE must never be echoed back")
}

// TestReadLLMEnvFile_ParsesKeyValueLines proves the dotenv-shaped parse:
// blank lines and #-comments skipped, KEY=VALUE kept verbatim (a value may
// itself contain "=", e.g. a base64 secret).
func TestReadLLMEnvFile_ParsesKeyValueLines(t *testing.T) {
	env, err := parseEnvFile(bytes.NewBufferString("# comment\n\nA=1\nB=two=parts\n  C = spaced \n"))
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"A": "1", "B": "two=parts", "C": "spaced"}, env)
}

func TestReadLLMEnvFile_RejectsLineWithNoEquals(t *testing.T) {
	_, err := parseEnvFile(bytes.NewBufferString("NOTKEYVALUE\n"))
	require.Error(t, err)
}

// TestRunLLMCreate_EnvFileNeverTakesArgv is the end-to-end CLI proof that
// --env-file is the ONLY way to set credentials: no flag accepts a literal
// secret, so nothing in argv (shell history, the process table, a CI log
// capturing the command line) can ever carry one.
func TestRunLLMCreate_EnvFileNeverTakesArgv(t *testing.T) {
	agentProject(t, "version: 6\n")
	t.Setenv("HOME", t.TempDir())

	envFile := filepath.Join(t.TempDir(), "big.env")
	require.NoError(t, os.WriteFile(envFile, []byte("OPENAI_API_KEY=sk-real-secret-value\n"), 0o600))

	cmd, out := textCmd()
	registerLLMWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse([]string{"--type", "codex", "--env-file", envFile}))

	require.NoError(t, runLLMCreate(cmd, []string{"big"}))
	assert.Contains(t, out.String(), "OPENAI_API_KEY")
	assert.NotContains(t, out.String(), "sk-real-secret-value")

	config.Invalidate()
	cfg, err := GetConfig()
	require.NoError(t, err)
	assert.Equal(t, "sk-real-secret-value", cfg.LabelEnv("big")["OPENAI_API_KEY"], "the real value must still be recorded for launch to use")
}
