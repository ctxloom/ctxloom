package opencode

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpencodeConfig_BackendType(t *testing.T) {
	assert.Equal(t, "opencode", OpencodeConfig{}.BackendType())
}

func TestNewOpencode_Defaults(t *testing.T) {
	b := NewOpencode()
	assert.Equal(t, "opencode", b.Name())
	assert.Equal(t, "opencode", b.BinaryPath)
}

func TestOpencode_Configure(t *testing.T) {
	b := NewOpencode()
	b.Configure(&OpencodeConfig{
		Model:      "openrouter/meta-llama/llama-3.3-70b-instruct:free",
		BinaryPath: "/home/babbitt/.opencode/bin/opencode",
		Args:       []string{"--log-level", "ERROR"},
		Env:        map[string]string{"FOO": "bar"},
	})
	assert.Equal(t, "/home/babbitt/.opencode/bin/opencode", b.BinaryPath)
	assert.Equal(t, []string{"--log-level", "ERROR"}, b.Args)
	assert.Equal(t, "bar", b.Env["FOO"])
	assert.Equal(t, "openrouter/meta-llama/llama-3.3-70b-instruct:free", b.model)
}

func TestOpencode_ConfigureIgnoresForeignConfig(t *testing.T) {
	b := NewOpencode()
	b.Configure(nil)
	assert.Equal(t, "opencode", b.BinaryPath)
}

// TestChatACPConfig_NoModelFlag pins that opencode's ACP config carries NO model
// delivery via argv/env: `opencode acp` has no --model flag (it treats the
// unknown flag as a usage error and exits without starting the server), so the
// model rides opencode.json, never the driver's --model/-c/env mechanisms.
func TestChatACPConfig_NoModelFlag(t *testing.T) {
	b := NewOpencode()
	b.Configure(&OpencodeConfig{BinaryPath: "/opt/opencode", Env: map[string]string{"K": "v"}})
	cfg := b.chatACPConfig()
	assert.Equal(t, "/opt/opencode acp", cfg.Command, "honors configured binary_path")
	assert.Empty(t, cfg.Model, "model rides opencode.json, not --model argv")
	assert.Empty(t, cfg.ModelConfigKey)
	assert.Empty(t, cfg.ModelEnvVar)
	assert.Empty(t, cfg.Agent, "opencode acp rejects --agent")
	assert.Empty(t, cfg.AgentEngine, "opencode acp rejects --agent-engine")
	assert.Equal(t, map[string]string{"K": "v"}, cfg.Env)
}

func TestChatACPConfig_DefaultBinary(t *testing.T) {
	b := NewOpencode()
	assert.Equal(t, "opencode acp", b.chatACPConfig().Command)
}

// TestWriteModelConfig_CreatesNewFile: a missing file is created with just the
// model key.
func TestWriteModelConfig_CreatesNewFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/work", 0o755))

	require.NoError(t, writeModelConfig(fs, "/work", "openrouter/x:free"))

	got := readJSON(t, fs, "/work/opencode.json")
	assert.Equal(t, "openrouter/x:free", got["model"])
	assert.Len(t, got, 1)
}

// TestWriteModelConfig_PreservesUnrelatedKeys: writing the model key leaves every
// other existing key untouched (read-modify-write, never overwrite).
func TestWriteModelConfig_PreservesUnrelatedKeys(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/work", 0o755))
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "mcp": {"local": {"type": "local", "command": ["foo"]}},
  "model": "openrouter/OLD:free"
}`
	require.NoError(t, afero.WriteFile(fs, "/work/opencode.json", []byte(existing), 0o644))

	require.NoError(t, writeModelConfig(fs, "/work", "openrouter/NEW:free"))

	got := readJSON(t, fs, "/work/opencode.json")
	assert.Equal(t, "openrouter/NEW:free", got["model"], "model key is updated")
	assert.Equal(t, "https://opencode.ai/config.json", got["$schema"], "unrelated $schema preserved")
	assert.Equal(t, "tokyonight", got["theme"], "unrelated theme preserved")
	assert.NotNil(t, got["mcp"], "unrelated nested mcp preserved")
	mcp := got["mcp"].(map[string]any)
	assert.NotNil(t, mcp["local"], "nested value preserved verbatim")
}

// TestWriteModelConfig_MalformedFileErrors: a malformed existing file must FAIL
// LOUDLY and leave the original bytes intact, never clobber them.
func TestWriteModelConfig_MalformedFileErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/work", 0o755))
	garbage := `{ this is not: valid json ]`
	require.NoError(t, afero.WriteFile(fs, "/work/opencode.json", []byte(garbage), 0o644))

	err := writeModelConfig(fs, "/work", "openrouter/x:free")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed")

	// The original malformed bytes must be untouched (no clobber).
	after, readErr := afero.ReadFile(fs, "/work/opencode.json")
	require.NoError(t, readErr)
	assert.Equal(t, garbage, string(after))
}

func readJSON(t *testing.T, fs afero.Fs, path string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, filepath.Clean(path))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}
