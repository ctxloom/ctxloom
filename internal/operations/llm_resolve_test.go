package operations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// captureStderr is defined in sync_security_test.go and reused here.

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
			out := captureStderr(t, func() { bc = DecodeBackendConfig(cfg, "gem") })
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

	out := captureStderr(t, func() { DecodeBackendConfig(cfg, "x") })
	assert.Contains(t, out, `unknown LLM backend type "nope"`)
	assert.NotContains(t, out, "not supported in this release")
}

// TestDecodeBackendConfig_RemovedBackendHintRidesTheDiagnosticChannel pins
// that DecodeBackendConfig emits every line — the primary decode failure AND
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

	out := captureStderr(t, func() { DecodeBackendConfig(cfg, "gem") })

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
