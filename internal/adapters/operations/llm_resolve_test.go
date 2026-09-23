package operations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// captureStderr is defined in sync_security_test.go and reused here.

// A label typed "gemini" names no engine this release has, and config authors
// may still carry one. It must degrade like any unknown type: the ordinary
// decode-failure warning, and no config.
func TestDecodeBackendConfig_GeminiTypeWarnsAsUnknown(t *testing.T) {
	cfg := gatedFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"gem": {Type: "gemini", Body: map[string]interface{}{}},
			},
		},
	})

	var bc interface{}
	out := captureStderr(t, func() { bc = DecodeBackendConfig(cfg, "gem") })
	assert.Nil(t, bc, "an unknown backend type degrades to nil")
	assert.Contains(t, out, `LLM config "gem": unknown LLM backend type "gemini"`)
}

// TestDecodeBackendConfig_DecodeFailureRidesTheDiagnosticChannel pins that
// DecodeBackendConfig reports a decode failure through clidiag.Warn, never a
// raw fmt.Fprintf(os.Stderr, ...), so it honours the process-wide conventions
// clidiag owns.
//
// The consequence is measurable, not stylistic. With structured diagnostics on
// (what `--format json` selects), clidiag writes one JSON-Lines envelope per
// warning; a bare Fprintf drops raw prose into the same stream, so a client
// parsing that channel hits a line that is not JSON. The assertion is therefore
// on the PAYLOAD: every line ctxloom writes to the diagnostic channel must be
// one envelope, and the warning's content must still be in there.
func TestDecodeBackendConfig_DecodeFailureRidesTheDiagnosticChannel(t *testing.T) {
	cfg := gatedFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"x": {Type: "nope", Body: map[string]interface{}{}},
			},
		},
	})

	clidiag.SetStructured(true)
	t.Cleanup(func() { clidiag.SetStructured(false) })

	out := captureStderr(t, func() { DecodeBackendConfig(cfg, "x") })

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.NotEmpty(t, lines[0], "the decode failure must be reported")
	var sawWarning bool
	for _, line := range lines {
		var env map[string]any
		require.NoErrorf(t, json.Unmarshal([]byte(line), &env),
			"every diagnostic line must be a clidiag envelope, got: %s", line)
		if warning, _ := env["warning"].(string); strings.Contains(warning, `unknown LLM backend type "nope"`) {
			sawWarning = true
		}
	}
	assert.True(t, sawWarning, "the decode failure must survive on the structured channel")
}

// TestMockControlFor_ReadsTheMockLabelsControlMapAndNothingElse pins the
// surviving request-env channel: the mock's `mock_control` map reaches the
// run request through its own key, while a real engine's label yields
// nothing — a real engine's environment is ambient, never config-declared
// (config.RetiredLLMEnvKey), so there is no map on its config to read.
func TestMockControlFor_ReadsTheMockLabelsControlMapAndNothingElse(t *testing.T) {
	cfg := gatedFixture(config.Fixture{
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"m": {Type: config.BackendMock, Body: map[string]interface{}{
					"mock_control": map[string]interface{}{"CTXLOOM_MOCK_RESPONSE": "canned-7f3a"},
				}},
				"big": {Type: "claude-code", Body: map[string]interface{}{"model": "opus"}},
			},
		},
	})

	assert.Equal(t, map[string]string{"CTXLOOM_MOCK_RESPONSE": "canned-7f3a"}, MockControlFor(cfg, "m"),
		"the mock label's mock_control map must reach the request env verbatim")
	assert.Nil(t, MockControlFor(cfg, "big"), "a real engine's label carries no request env map")
	assert.Nil(t, MockControlFor(cfg, "absent"), "an unset label carries none either")
}
