package cli

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns what
// fn wrote to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// twoLabelConfig builds a config with two labels of the same backend type —
// the shape where a map-ordered type scan would pick a random entry per
// process.
func twoLabelConfig() *config.Config {
	return config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"alpha": {Type: "claude-code", Body: map[string]interface{}{"binary_path": "/bin/alpha"}},
				"beta":  {Type: "claude-code", Body: map[string]interface{}{"binary_path": "/bin/beta"}},
			},
		},
	})
}

// decodedClaudeBinaryPath decodes the resolved claude-code config and returns
// BinaryPath — used as the distinguishing marker between the two labels
// (ClaudeConfig.Model was deleted as dead: decoded and never read
// by this package, the effective model resolves untyped elsewhere).
func decodedClaudeBinaryPath(t *testing.T, cfg *config.Config) string {
	t.Helper()
	bc := decodeBackendConfigForType(cfg, "claude-code")
	cc, ok := bc.(*claude.ClaudeConfig)
	require.True(t, ok, "expected *claude.ClaudeConfig, got %#v", bc)
	return cc.BinaryPath
}

func TestDecodeBackendConfigForType_PrefersPrimaryLabel(t *testing.T) {
	f := twoLabelConfig().ToFixture()
	f.LM.Defaults.Primary = "beta"
	cfg := config.NewFixture(f)

	assert.Equal(t, "/bin/beta", decodedClaudeBinaryPath(t, cfg))
}

func TestDecodeBackendConfigForType_DeterministicWithoutPrimary(t *testing.T) {
	cfg := twoLabelConfig()

	assert.Equal(t, "/bin/alpha", decodedClaudeBinaryPath(t, cfg),
		"ties resolve to the lexicographically first label")
}

func TestDecodeBackendConfigForType_PrimaryOfOtherTypeFallsBack(t *testing.T) {
	f := twoLabelConfig().ToFixture()
	f.LM.Configs["oc"] = config.LLMConfig{Type: "opencode", Body: map[string]interface{}{}}
	f.LM.Defaults.Primary = "oc"
	cfg := config.NewFixture(f)

	assert.Equal(t, "/bin/alpha", decodedClaudeBinaryPath(t, cfg),
		"a primary of a different type falls back to the sorted-label scan")
}

// TestIsMockBackend pins the one predicate both usableLLMs (run.go) and
// getAvailableEngines (init.go) now share: the "mock"
// backend must never surface in a user-facing engine list, and nothing else
// should be caught by the same check.
func TestIsMockBackend(t *testing.T) {
	assert.True(t, isMockBackend("mock"))
	assert.False(t, isMockBackend("claude-code"))
	assert.False(t, isMockBackend(""))
}
