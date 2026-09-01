package opencode

import (
	"encoding/json"
	"io"
	"os"
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

// TestOpencode_ConfigureThinkingIsWarnedNoOp pins the honest-no-op contract:
// opencode has no wired mechanism for the cross-engine normalized thinking
// level (unlike claude/codex), so an explicit `thinking` setting must WARN
// rather than silently vanish.
func TestOpencode_ConfigureThinkingIsWarnedNoOp(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w

	b := NewOpencode()
	b.Configure(&OpencodeConfig{Thinking: "high"})
	_ = w.Close()
	os.Stderr = orig

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "thinking", "an explicit setting must be surfaced, not silently swallowed")
	assert.Contains(t, string(out), "high")
}

func TestOpencode_ConfigureIgnoresForeignConfig(t *testing.T) {
	b := NewOpencode()
	b.Configure(nil)
	assert.Equal(t, "opencode", b.BinaryPath)
}

// writeModelConfig (the model-only projection of writeOpencodeConfig) was
// deleted as dead: test-only, and a single-expression pass-through. Its three
// tests here duplicated settings_test.go's direct
// writeOpencodeConfig coverage byte-for-byte:
// TestWriteOpencodeConfig_MergesManagedKeysPreservingForeign (new-file +
// preserve-unrelated-keys) and TestWriteOpencodeConfig_MalformedErrors
// (fail-loud, bytes untouched) — both already exercise managedConfig{model:
// ...} through the real merge engine, so no coverage was lost deleting these.

func readJSON(t *testing.T, fs afero.Fs, path string) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, filepath.Clean(path))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}
