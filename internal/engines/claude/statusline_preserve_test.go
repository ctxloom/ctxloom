package claude

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeStatusLineSeed runs one apply over a settings.json seeded with seed
// ("" for none) and returns the bytes left on disk.
func writeStatusLineSeed(t *testing.T, seed string) []byte {
	t.Helper()
	fs := afero.NewMemMapFs()
	w := &ClaudeCodeHookWriter{FS: fs}
	path := w.SettingsPath("/proj")
	require.NoError(t, fs.MkdirAll("/proj/.claude", 0o755))
	if seed != "" {
		testsupport.WriteFileString(t, fs, path, seed, 0o644)
	}
	require.NoError(t, w.WriteSettings(&wire.HooksConfig{}, ctxloomBundleMCP(), "/proj"))
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	return data
}

// statusLineKeys decodes the statusLine object of a settings document into its
// keys' raw bytes, each canonicalised (sorted, number literals kept verbatim)
// so input and output compare byte-for-byte regardless of indentation depth.
func statusLineKeys(t *testing.T, doc []byte) map[string]string {
	t.Helper()
	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(doc, &top))
	var sl map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["statusLine"], &sl))
	out := make(map[string]string, len(sl))
	for k, v := range sl {
		c, err := agent.CanonicalJSON(v)
		require.NoError(t, err)
		out[k] = string(bytes.TrimSpace(c))
	}
	return out
}

// TestWriteSettings_StatusLineVendorFieldsSurvive is the narrowing defect:
// claudeCodeStatusLine models only type/command/padding, and the statusLine was
// re-emitted from that struct alone, so every other key Claude Code supports
// (or will) was deleted from the user's file on the next apply — no warning,
// exit 0. The unknown keys must come back as the bytes that went in, nested
// structure and number literals included.
//
// Both ownership states are covered because they reach the write by different
// routes: a user statusline is left as parsed, while ctxloom's own is rebuilt
// by ensureStatusLine — which is where a fresh struct would drop the extras.
func TestWriteSettings_StatusLineVendorFieldsSurvive(t *testing.T) {
	const extras = `"refreshInterval": 5, "vendor": {"deep": [1, {"big": 1234567890123456789}], "flag": true}`

	for _, tc := range []struct{ name, command string }{
		{"a user statusline", "ccusage"},
		{"ctxloom's own statusline", ctxloomStatusLineCommand()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := `{"statusLine": {"type": "command", "command": "` + tc.command + `", ` + extras + `}}`
			in := statusLineKeys(t, []byte(seed))
			out := statusLineKeys(t, writeStatusLineSeed(t, seed))

			for _, k := range []string{"refreshInterval", "vendor"} {
				assert.Equal(t, in[k], out[k], "unknown statusLine key %q must survive the round trip byte-for-byte", k)
			}
			assert.Equal(t, `"`+tc.command+`"`, out["command"], "control: the modelled field is still the one written")
		})
	}
}

// TestWriteSettings_StatusLineBytesWithoutUnknownFieldsUnchanged pins the
// on-disk bytes for statusLines carrying only the modelled fields, so
// preserving unknown keys cannot re-shape the documents that have none.
func TestWriteSettings_StatusLineBytesWithoutUnknownFieldsUnchanged(t *testing.T) {
	for _, tc := range []struct{ name, seed, want string }{
		{"fresh install", "",
			"{\n  \"statusLine\": {\n    \"command\": \"ctxloom hook hud\",\n    \"type\": \"command\"\n  }\n}\n"},
		{"user statusline with padding", `{"statusLine":{"type":"command","command":"ccusage","padding":2}}`,
			"{\n  \"statusLine\": {\n    \"command\": \"ccusage\",\n    \"padding\": 2,\n    \"type\": \"command\"\n  }\n}\n"},
		{"ctxloom's own statusline", `{"statusLine":{"type":"command","command":"ctxloom hook hud","padding":2}}`,
			"{\n  \"statusLine\": {\n    \"command\": \"ctxloom hook hud\",\n    \"type\": \"command\"\n  }\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(writeStatusLineSeed(t, tc.seed)))
		})
	}
}
