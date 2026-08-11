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

// A label typed "gemini" (the pre-v4 name) or "antigravity" (its v4
// successor, removed outright in 0.7.0) must degrade like any unknown type,
// with a warning that says the backend is unsupported rather than pointing
// at a replacement that no longer exists either.
func TestDecodeBackendConfig_RemovedGeminiAndAntigravityTypesHintUnsupported(t *testing.T) {
	for _, removedType := range []string{"gemini", "antigravity"} {
		t.Run(removedType, func(t *testing.T) {
			cfg := config.NewFixture(config.Fixture{
				LM: config.LMConfig{
					Configs: map[string]config.LLMConfig{
						"gem": {Type: removedType, Body: map[string]interface{}{}},
					},
				},
			})

			var bc interface{}
			out := captureStderr(t, func() { bc = decodeBackendConfig(cfg, "gem") })
			assert.Nil(t, bc, "removed backend type degrades to nil")
			assert.Contains(t, out, `unknown LLM backend type "`+removedType+`"`)
			assert.Contains(t, out, "not supported in this release", "warning should say the backend is unsupported")
			assert.Contains(t, out, "claude-code")
		})
	}
}

// Other unknown types keep the plain warning — no removed-backend hint.
func TestDecodeBackendConfig_UnknownTypeHasNoRemovedBackendHint(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"x": {Type: "nope", Body: map[string]interface{}{}},
			},
		},
	})

	out := captureStderr(t, func() { decodeBackendConfig(cfg, "x") })
	assert.Contains(t, out, `unknown LLM backend type "nope"`)
	assert.NotContains(t, out, "not supported in this release")
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

// TestDecodeBackendConfig_RemovedBackendHintRidesTheDiagnosticChannel pins
// that decodeBackendConfig emits every line — the primary decode failure AND
// the removed-backend hint — through clidiag.Warn, never a raw
// fmt.Fprintf(os.Stderr, ...), so both honour the process-wide conventions
// clidiag owns.
//
// The consequence is measurable, not stylistic. With structured diagnostics on
// (what `--format json` selects), clidiag writes one JSON-Lines envelope per
// warning; a bare Fprintf drops raw prose into the same stream, so a client
// parsing that channel hits a line that is not JSON. The assertion is therefore
// on the PAYLOAD: every line ctxloom writes to the diagnostic channel must be
// one envelope, and the hint's content must still be in there.
func TestDecodeBackendConfig_RemovedBackendHintRidesTheDiagnosticChannel(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"gem": {Type: "antigravity", Body: map[string]interface{}{}},
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
		if warning, _ := env["warning"].(string); strings.Contains(warning, "not supported in this release") {
			sawHint = true
			assert.Contains(t, warning, "claude-code")
		}
	}
	assert.True(t, sawHint, "the removed-backend hint must survive on the structured channel")
}
