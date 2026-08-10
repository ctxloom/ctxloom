// Plugin discovery tests verify that ctxloom correctly identifies built-in LM plugins
// (claude-code, antigravity, codex) and any user-configured plugins. This is essential
// for the `ctxloom run` command to know which backends are available for context injection.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
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
	if !isKnownLLM(cfg, "codex") {
		t.Error("expected codex to be known")
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
// real HOME/project dir touched), plus the Manager targeting that SAME
// filesystem/appDir, for the `llm default <name>` set-path tests: cfg serves
// the read-only known-LLM check, mgr is what SetDefaultLLM's transaction
// actually writes through.
//
// The seeded config.yaml names an explicit starting primary ("codex") rather
// than leaving llm.defaults.primary absent: an absent primary is filled
// in-memory by the shipped-default overlay (mergeDefaultConfig) at load
// time, which would make "claude-code" look already-current the moment
// Manager.Update's transaction re-reads the file, turning every "set
// claude-code" test below into an "unchanged" one instead.
func memConfig(t *testing.T) (*config.Config, *config.Manager) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte("version: 5\nllm:\n  defaults:\n    primary: codex\n"), 0o644))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	cfg.SetFS(fs)
	mgr := config.NewManager(config.WithFS(fs), config.WithAppDir(appDir))
	return cfg, mgr
}

func TestRunLLMDefault_Show_Text_PrintsCurrentDefault(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{LM: config.LMConfig{Defaults: config.RoleDefaults{Primary: "claude-code"}}})
	cmd, out := llmDefaultTestCmd("text")

	require.NoError(t, runLLMDefault(cmd, nil, cfg, nil))
	assert.Equal(t, "claude-code\n", out.String())
}

func TestRunLLMDefault_Show_JSON_EmitsStructuredResult(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{LM: config.LMConfig{Defaults: config.RoleDefaults{Primary: "claude-code"}}})
	cmd, out := llmDefaultTestCmd("json")

	require.NoError(t, runLLMDefault(cmd, nil, cfg, nil))
	var result llmDefaultShowResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "claude-code", result.Default)
}

func TestRunLLMDefault_Set_JSON_EmitsSetDefaultLLMResult(t *testing.T) {
	cfg, mgr := memConfig(t)
	cmd, out := llmDefaultTestCmd("json")

	require.NoError(t, runLLMDefault(cmd, mgr, cfg, []string{"claude-code"}))

	var result operations.SetDefaultLLMResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	assert.Equal(t, "set", result.Status)
	assert.Equal(t, "claude-code", result.Name)
}

func TestRunLLMDefault_Set_Text_PrintsSetMessage(t *testing.T) {
	cfg, mgr := memConfig(t)
	cmd, out := llmDefaultTestCmd("text")

	require.NoError(t, runLLMDefault(cmd, mgr, cfg, []string{"claude-code"}))
	assert.Equal(t, "Default LLM set to: claude-code\n", out.String())
}

func TestRunLLMDefault_Set_UnknownLLM_Errors(t *testing.T) {
	cfg := &config.Config{}
	cmd, _ := llmDefaultTestCmd("text")

	err := runLLMDefault(cmd, nil, cfg, []string{"not-a-real-llm"})
	require.Error(t, err)
}

// TestIsKnownLLM_AgreesWithTheAdvertisedSet pins that `llm default
// <name>` decided membership with its own predicate (backends.Exists ||
// cfg.GetLLMEntry) and then built the rejection message from
// operations.AvailableLLMNames — two definitions of "known LLM" with no
// compiler help. A drift between them is a self-contradicting command: it
// either rejects a name its own error message lists as available, or accepts
// one the listing never mentions.
//
// The two sets were VERBATIM equivalent when this was written, so no parity
// assertion here could be red; the test's job is to pin the equivalence
// permanently, over the shapes that would break it — a builtin, the mock double
// registered into the production table, a config-only label, a label whose
// backend type is unknown, an empty name, and a plain typo.
func TestIsKnownLLM_AgreesWithTheAdvertisedSet(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"my-claude": {Type: "claude-code", Body: map[string]interface{}{}},
				"stale":     {Type: "gemini", Body: map[string]interface{}{}},
			},
		},
	})

	for _, name := range []string{
		"claude-code", "codex", "kiro", "opencode", mockBackendName,
		"my-claude", "stale", "nonexistent-plugin", "",
	} {
		advertised := slices.Contains(operations.AvailableLLMNames(cfg), name)
		assert.Equalf(t, advertised, isKnownLLM(cfg, name),
			"%q: the set `llm default` accepts must be the set it advertises", name)
	}
}
