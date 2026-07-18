// Plugin discovery tests verify that ctxloom correctly identifies built-in LM plugins
// (claude-code, antigravity, codex) and any user-configured plugins. This is essential
// for the `ctxloom run` command to know which backends are available for context injection.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// =============================================================================
// Plugin Recognition Tests
// =============================================================================
// ctxloom must recognize built-in plugins without explicit configuration,
// while rejecting unknown plugin names to prevent typos.

func TestIsKnownLLM_BuiltIn(t *testing.T) {
	// Built-in plugins must be recognized even with empty config
	cfg := &config.Config{}
	if !isKnownLLM(cfg, "claude-code") {
		t.Error("expected claude-code to be known")
	}
	if !isKnownLLM(cfg, "antigravity") {
		t.Error("expected antigravity to be known")
	}
}

func TestIsKnownLLM_Unknown(t *testing.T) {
	// Unknown plugins should be rejected to catch typos early
	cfg := &config.Config{}
	if isKnownLLM(cfg, "nonexistent-plugin") {
		t.Error("expected nonexistent-plugin to be unknown")
	}
}

// Plugin listing tests (operations.AvailableLLMNames) moved to
// internal/operations/llm_test.go alongside the function (ISO0 extraction).

// llmDefaultTestCmd mirrors testCmd (trust_test.go) but also registers the
// --format flag so tests can drive both text and structured output through
// the same runLLMDefault body.
func llmDefaultTestCmd(format string) (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.Flags().String("format", "text", "")
	_ = c.Flags().Set("format", format)
	var buf bytes.Buffer
	c.SetOut(&buf)
	return c, &buf
}

// memConfig returns a config.Config backed by an in-memory filesystem (no
// real HOME/project dir touched) that Save() can write to, for the `llm
// default <name>` set-path tests.
func memConfig(t *testing.T) *config.Config {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	cfg := &config.Config{AppPaths: []string{appDir}}
	cfg.SetFS(fs)
	return cfg
}

func TestRunLLMDefault_Show_Text_PrintsCurrentDefault(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetPrimaryLabel("claude-code")
	cmd, out := llmDefaultTestCmd("text")

	require.NoError(t, runLLMDefault(cmd, cfg, nil))
	assert.Equal(t, "claude-code\n", out.String())
}

func TestRunLLMDefault_Show_JSON_EmitsStructuredResult(t *testing.T) {
	cfg := &config.Config{}
	cfg.SetPrimaryLabel("claude-code")
	cmd, out := llmDefaultTestCmd("json")

	require.NoError(t, runLLMDefault(cmd, cfg, nil))
	var result llmDefaultShowResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "claude-code", result.Default)
}

func TestRunLLMDefault_Set_JSON_EmitsSetDefaultLLMResult(t *testing.T) {
	cfg := memConfig(t)
	cmd, out := llmDefaultTestCmd("json")

	require.NoError(t, runLLMDefault(cmd, cfg, []string{"claude-code"}))

	var result operations.SetDefaultLLMResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "set", result.Status)
	assert.Equal(t, "claude-code", result.Name)
}

func TestRunLLMDefault_Set_Text_PrintsSetMessage(t *testing.T) {
	cfg := memConfig(t)
	cmd, out := llmDefaultTestCmd("text")

	require.NoError(t, runLLMDefault(cmd, cfg, []string{"claude-code"}))
	assert.Equal(t, "Default LLM set to: claude-code\n", out.String())
}

func TestRunLLMDefault_Set_UnknownLLM_Errors(t *testing.T) {
	cfg := &config.Config{}
	cmd, _ := llmDefaultTestCmd("text")

	err := runLLMDefault(cmd, cfg, []string{"not-a-real-llm"})
	require.Error(t, err)
}
