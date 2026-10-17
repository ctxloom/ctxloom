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
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agents"
)

func TestCheckLLMExistence_EachVerbRefusesTheOthersCase(t *testing.T) {
	agentProject(t, "schema_version: 8\nllm:\n  configs:\n    big: { type: claude-code, model: opus }\n")
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
	agentProject(t, "schema_version: 8\n")
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
	entry := &operations.LLMEntry{Label: "big", Type: "mock", Model: "o1", Permissions: agents.LabelPermissions{Engine: map[string]any{"mode": "bypass"}}}

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

// runLLMWrite parses args against create's (or edit's) real flag set and runs
// the shared write body, as one CLI invocation would.
func runLLMWrite(t *testing.T, edit bool, args ...string) error {
	t.Helper()
	t.Cleanup(func() { llmHome, llmSetFields = false, nil })
	cmd, _ := textCmd()
	registerLLMWriteFlags(cmd)
	require.NoError(t, cmd.Flags().Parse(args))
	return writeLLM(cmd, cmd.Flags().Arg(0), edit)
}

// readYAMLFile parses a config file into a generic map, so an assertion
// compares what was WRITTEN, not what a loader made of it.
func readYAMLFile(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test's own temp file
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(b, &m))
	return m
}

// referenceLabelArgs creates the opencode label a LAN endpoint needs, the
// whole of it from the CLI.
var referenceLabelArgs = []string{
	"qwen", "--type", "opencode", "--model", "qwen3.8-27b",
	"--set", "context_window=131072",
	"--set", "provider.base_url=http://192.168.38.165:8081/v1",
	"--set", "provider.output_limit=32768",
}

var referenceLabel = map[string]any{
	"type": "opencode", "model": "qwen3.8-27b", "context_window": 131072,
	"provider": map[string]any{"base_url": "http://192.168.38.165:8081/v1", "output_limit": 32768},
}

func llmConfigsIn(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	llm, _ := doc["llm"].(map[string]any)
	configs, _ := llm["configs"].(map[string]any)
	return configs
}

// TestLLMCreate_HomeWritesTheWholeOpencodeLabelToTheUserConfig: an opencode
// label against an OpenAI-compatible endpoint is created from the CLI alone,
// in the user-global config, and the project's config is not touched.
func TestLLMCreate_HomeWritesTheWholeOpencodeLabelToTheUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const projectYAML = "schema_version: 8\n"
	root := agentProject(t, projectYAML)

	require.NoError(t, runLLMWrite(t, false, append([]string{"--home"}, referenceLabelArgs...)...))

	got := llmConfigsIn(t, readYAMLFile(t, filepath.Join(home, ".ctxloom", "config.yaml")))
	assert.Equal(t, referenceLabel, got["qwen"])

	project, err := os.ReadFile(filepath.Join(root, ".ctxloom", "config.yaml")) //nolint:gosec // the test's project
	require.NoError(t, err)
	assert.Equal(t, projectYAML, string(project), "--home must leave the project's config untouched")

	resetApp()
	cfg, err := GetConfig()
	require.NoError(t, err)
	_, ok := cfg.GetLLMEntry("qwen")
	assert.True(t, ok, "the project reads the user-global label")
}

// TestLLMCreate_DefaultScopeIsTheProject: without --home the label lands in
// the project's config, as before.
func TestLLMCreate_DefaultScopeIsTheProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := agentProject(t, "schema_version: 8\n")

	require.NoError(t, runLLMWrite(t, false, referenceLabelArgs...))

	got := llmConfigsIn(t, readYAMLFile(t, filepath.Join(root, ".ctxloom", "config.yaml")))
	assert.Equal(t, referenceLabel, got["qwen"])
	assert.NoFileExists(t, filepath.Join(home, ".ctxloom", "config.yaml"))
}

// TestLLMWrite_SchemaRefusesAndNothingIsWritten: a key the label's engine does
// not take, a value of the wrong type, and a provider block missing what the
// schema requires are each refused with the schema's message, and the file is
// left exactly as it was.
func TestLLMWrite_SchemaRefusesAndNothingIsWritten(t *testing.T) {
	cases := map[string]struct {
		set  []string
		want string
	}{
		"unknown key":            {[]string{"--set", "context_windw=131072"}, "context_windw"},
		"wrong type":             {[]string{"--set", "context_window=lots"}, "context_window"},
		"provider wants limit":   {[]string{"--set", "context_window=1", "--set", "provider.base_url=http://h/v1"}, "output_limit"},
		"provider wants window":  {[]string{"--set", "provider.base_url=http://h/v1", "--set", "provider.output_limit=1"}, "context_window"},
		"bad url":                {[]string{"--set", "context_window=1", "--set", "provider.base_url=h/v1", "--set", "provider.output_limit=1"}, "base_url"},
		"flag-owned key":         {[]string{"--set", "type=claude-code"}, "--type"},
		"bad neutral permission": {[]string{"--set", "permissions.approver=nobody"}, "approver"},
		"bad engine permission":  {[]string{"--set", "permissions.mode=yolo"}, "yolo"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			const projectYAML = "schema_version: 8\n"
			root := agentProject(t, projectYAML)

			args := append([]string{"qwen", "--type", "opencode", "--model", "m"}, tc.set...)
			err := runLLMWrite(t, false, args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)

			b, rerr := os.ReadFile(filepath.Join(root, ".ctxloom", "config.yaml")) //nolint:gosec // the test's project
			require.NoError(t, rerr)
			assert.Equal(t, projectYAML, string(b), "a refused write must write nothing")
		})
	}
}

// TestLLMEdit_SetChangesAndEmptyRemovesAField: edit applies --set onto the
// existing entry, keeps what it does not name, and an empty value removes a
// key.
func TestLLMEdit_SetChangesAndEmptyRemovesAField(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := agentProject(t, "schema_version: 8\n")
	require.NoError(t, runLLMWrite(t, false, referenceLabelArgs...))

	require.NoError(t, runLLMWrite(t, true, "qwen",
		"--set", "provider.header_timeout_ms=0",
		"--set", "model_options={chat_template_kwargs: {enable_thinking: false}}",
		"--set", "share=disabled",
		"--set", "permissions.approver=none"))
	require.NoError(t, runLLMWrite(t, true, "qwen", "--set", "share="))

	got := llmConfigsIn(t, readYAMLFile(t, filepath.Join(root, ".ctxloom", "config.yaml")))["qwen"].(map[string]any)
	assert.Equal(t, map[string]any{"base_url": "http://192.168.38.165:8081/v1", "output_limit": 32768, "header_timeout_ms": 0}, got["provider"])
	assert.Equal(t, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}, got["model_options"])
	assert.NotContains(t, got, "share", "an empty value removes the key")
	assert.Equal(t, map[string]any{"approver": "none"}, got["permissions"], "--set reaches the permissions block")
	assert.Equal(t, 131072, got["context_window"], "a key edit did not name survives")
}

// TestLLMRemove_HomeRemovesFromTheUserConfig: remove takes the same scope.
func TestLLMRemove_HomeRemovesFromTheUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	agentProject(t, "schema_version: 8\n")
	require.NoError(t, runLLMWrite(t, false, append([]string{"--home"}, referenceLabelArgs...)...))

	llmHome, llmRemoveYes = true, true
	t.Cleanup(func() { llmHome, llmRemoveYes = false, false })
	cmd, _ := textCmd()
	require.NoError(t, runLLMRemove(cmd, []string{"qwen"}))

	assert.NotContains(t, llmConfigsIn(t, readYAMLFile(t, filepath.Join(home, ".ctxloom", "config.yaml"))), "qwen")
}
