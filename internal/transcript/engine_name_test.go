package transcript

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// registeredClaudeBackendName is the literal internal/claude/claudecode.go
// hands agent.NewBaseBackend, and therefore the literal that reaches
// NewRecorder in production: GRPCClient.openRecorder passes the plugin's own
// LLMInfo.Name (internal/lm/grpc/chat.go), and coord.EngineHost passes the
// backend name RunnerHello advertised (enginehost.go). Neither normalizes.
const registeredClaudeBackendName = "claude-code"

// schemaEngineNameForClaude is the only claude spelling
// docs/transcript.schema.json's `engine` enum admits — the spelling every
// testdata fixture carries and the one no production writer ever emits.
const schemaEngineNameForClaude = "claude"

// readRecordedRawLines returns a harp's canonical transcript file as its raw
// JSON lines. Schema validation has to see the BYTES on disk, not a re-marshal
// of a decoded Record — a round trip through the Go type would launder exactly
// the kind of divergence this file exists to catch.
func readRecordedRawLines(t *testing.T, harp string) []string {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	require.NotEmpty(t, out, "expected at least one recorded line")
	return out
}

// TestRecorder_EngineIsWrittenVerbatimAndClaudeFailsThePublishedSchema is the
// standing tripwire for the engine-name discrepancy, which is ESCALATED rather than fixed: the
// registered-backend-name vocabulary ("claude-code") and the published
// transcript schema's `engine` enum ("claude") disagree, and deciding which
// one is canonical is a contract call, not a sweep's.
//
// The two halves together are the whole mechanism, and neither is sufficient
// alone:
//
//   - the recorder writes `engine` VERBATIM from its constructor argument —
//     there is no normalization, allowlist or refusal anywhere on the path;
//   - the value production actually supplies for claude is rejected by
//     docs/transcript.schema.json, while the fixtures' spelling is accepted.
//
// So every claude transcript this recorder writes violates the shipped
// schema, and TestFixtures_ConformToJSONSchema cannot see it because the
// fixtures are hand-authored with the spelling production never emits.
//
// This test asserts the CURRENT, divergent state deliberately. It is written
// to fail the moment either side moves — a normalization step in the
// recorder, or a widened enum in the schema — so whichever way the escalation
// is decided, the decision cannot land silently.
func TestRecorder_EngineIsWrittenVerbatimAndClaudeFailsThePublishedSchema(t *testing.T) {
	// compileTranscriptSchema reads docs/ relative to this source file, so it
	// must run BEFORE HOME is rerooted; Isolate only moves HOME, but ordering
	// it first keeps the dependency obvious.
	schema := compileTranscriptSchema(t)
	testsupport.Isolate(t)

	// The enum's own membership, stated once so the rest of the test is about
	// the recorder rather than about JSON Schema.
	require.Error(t, schema.Validate(engineProbeLine(t, registeredClaudeBackendName)),
		"schema must reject the registered backend name; if this passes the enum was widened and the engine-name discrepancy was decided")
	require.NoError(t, schema.Validate(engineProbeLine(t, schemaEngineNameForClaude)),
		"schema must accept the fixtures' spelling")

	harp := "u144f05-engine-name-harp"
	writeOneClaudeRecord(t, harp)

	lines := readRecordedRawLines(t, harp)
	require.Len(t, lines, 1)

	var decoded Record
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &decoded))
	assert.Equal(t, registeredClaudeBackendName, decoded.Engine,
		"the recorder passes engine through verbatim; a mismatch here means normalization was added and the engine-name discrepancy was decided")

	var onDisk interface{}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &onDisk))
	err := schema.Validate(onDisk)
	require.Error(t, err, "a real claude line is currently schema-INVALID — that is the finding, not a passing state")
	assert.Contains(t, err.Error(), "engine",
		"the schema failure must be the engine enum and nothing else")
}

// writeOneClaudeRecord drives a real Recorder exactly as production does for
// claude: constructed with the registered backend name, fed one ordinary
// assistant entry.
func writeOneClaudeRecord(t *testing.T, harp string) {
	t.Helper()
	rec, err := NewRecorder(harp, registeredClaudeBackendName)
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:    agent.EntryTypeAssistant,
		Content: "ping",
	}}))
	require.NoError(t, rec.Close())
}

// engineProbeLine builds the smallest schema-complete record carrying engine,
// so a validation failure can only be about the enum.
func engineProbeLine(t *testing.T, engine string) interface{} {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"v":      SchemaVersion,
		"harp":   "probe-harp",
		"engine": engine,
		"seq":    0,
		"ts":     "2026-07-31T00:00:00Z",
		"kind":   string(KindEntry),
		"entry":  map[string]interface{}{"type": "assistant", "content": "ping"},
	})
	require.NoError(t, err)
	var v interface{}
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}
