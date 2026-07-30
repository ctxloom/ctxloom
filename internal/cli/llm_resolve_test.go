package cli

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
// (ClaudeConfig.Model was deleted as dead, U032-F14: decoded and never read
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

// A label still typed "gemini" (config migration not yet run/committed) must
// degrade like any unknown type, but the warning points at the replacement.
func TestDecodeBackendConfig_RemovedGeminiTypeHintsAntigravity(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"gem": {Type: "gemini", Body: map[string]interface{}{"model": "gemini-2.5-flash"}},
			},
		},
	})

	var bc interface{}
	out := captureStderr(t, func() { bc = decodeBackendConfig(cfg, "gem") })
	assert.Nil(t, bc, "removed backend type degrades to nil")
	assert.Contains(t, out, `unknown LLM backend type "gemini"`)
	assert.Contains(t, out, "replaced by", "warning should explain the gemini→antigravity replacement")
	assert.Contains(t, out, "antigravity")
}

// Other unknown types keep the plain warning — no gemini hint.
func TestDecodeBackendConfig_UnknownTypeHasNoGeminiHint(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"x": {Type: "nope", Body: map[string]interface{}{}},
			},
		},
	})

	out := captureStderr(t, func() { decodeBackendConfig(cfg, "x") })
	assert.Contains(t, out, `unknown LLM backend type "nope"`)
	assert.NotContains(t, out, "antigravity")
}

func TestDecodeBackendConfigForType_PrimaryOfOtherTypeFallsBack(t *testing.T) {
	f := twoLabelConfig().ToFixture()
	f.LM.Configs["agy"] = config.LLMConfig{Type: "antigravity", Body: map[string]interface{}{}}
	f.LM.Defaults.Primary = "agy"
	cfg := config.NewFixture(f)

	assert.Equal(t, "/bin/alpha", decodedClaudeBinaryPath(t, cfg),
		"a primary of a different type falls back to the sorted-label scan")
}

// TestIsMockBackend pins the one predicate both usableLLMs (run.go) and
// getAvailableEngines (init.go) now share (U057-F14 escalation): the "mock"
// backend must never surface in a user-facing engine list, and nothing else
// should be caught by the same check.
func TestIsMockBackend(t *testing.T) {
	assert.True(t, isMockBackend("mock"))
	assert.False(t, isMockBackend("claude-code"))
	assert.False(t, isMockBackend(""))
}

// TestDecodeBackendConfig_GeminiHintRidesTheDiagnosticChannel pins U037-F25.
// decodeBackendConfig emitted its first line through clidiag.Warn and the
// gemini-successor hint on the line below through a raw
// fmt.Fprintf(os.Stderr, ...) — two diagnostic channels in one function, and
// only one of them honours the process-wide conventions clidiag owns.
//
// The consequence is measurable, not stylistic. With structured diagnostics on
// (what `--format json` selects), clidiag writes one JSON-Lines envelope per
// warning; a bare Fprintf drops raw prose into the same stream, so a client
// parsing that channel hits a line that is not JSON. The assertion is therefore
// on the PAYLOAD: every line ctxloom writes to the diagnostic channel must be
// one envelope, and the hint's content must still be in there.
func TestDecodeBackendConfig_GeminiHintRidesTheDiagnosticChannel(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"gem": {Type: "gemini", Body: map[string]interface{}{"model": "gemini-2.5-flash"}},
			},
		},
	})

	clidiag.SetStructured(true)
	t.Cleanup(func() { clidiag.SetStructured(false) })

	out := captureStderr(t, func() { decodeBackendConfig(cfg, "gem") })

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.NotEmpty(t, lines[0], "the decode failure must be reported")
	var sawHint bool
	for _, line := range lines {
		var env map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &env),
			"every diagnostic line must be a clidiag envelope, got: %s", line)
		if warning, _ := env["warning"].(string); strings.Contains(warning, "replaced by") {
			sawHint = true
			assert.Contains(t, warning, "antigravity")
		}
	}
	assert.True(t, sawHint, "the gemini→antigravity hint must survive on the structured channel")
}
