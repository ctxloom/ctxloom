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

// registeredMockBackendName is the test double's registry name. It is a
// production-reachable writer (`--llm mock` reaches transcript.RecordOneshot
// through internal/operations), so the schema must admit it too.
const registeredMockBackendName = "mock"

// unregisteredClaudeShortName is the short spelling no backend registers and
// no production writer ever emits. The schema must NOT admit it: the `engine`
// enum is the registry's vocabulary, and admitting a second spelling for one
// engine would let a fixture pass under a name a real line never carries.
const unregisteredClaudeShortName = "claude"

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

// TestRecorder_EngineIsWrittenVerbatimAndValidatesAgainstThePublishedSchema
// pins the engine-name contract from both sides:
//
//   - the recorder writes `engine` VERBATIM from its constructor argument —
//     there is no normalization, allowlist or refusal anywhere on the path;
//   - docs/transcript.schema.json's `engine` enum speaks the backend
//     REGISTRY's vocabulary, so the value production actually supplies is
//     admitted and the short spelling nothing registers is rejected.
//
// Together they mean a real claude line written by this recorder validates
// against the shipped schema, and that no fixture can pass under a spelling
// production never emits. Either side moving — a normalization step in the
// recorder, or the enum drifting back to a short name — fails here.
func TestRecorder_EngineIsWrittenVerbatimAndValidatesAgainstThePublishedSchema(t *testing.T) {
	// compileTranscriptSchema reads docs/ relative to this source file, so it
	// must run BEFORE HOME is rerooted; Isolate only moves HOME, but ordering
	// it first keeps the dependency obvious.
	schema := compileTranscriptSchema(t)
	testsupport.Isolate(t)

	// The enum's own membership, stated once so the rest of the test is about
	// the recorder rather than about JSON Schema.
	require.NoError(t, schema.Validate(engineProbeLine(t, registeredClaudeBackendName)),
		"schema must admit the registered claude backend name")
	require.NoError(t, schema.Validate(engineProbeLine(t, registeredMockBackendName)),
		"schema must admit the registered mock backend name")
	require.Error(t, schema.Validate(engineProbeLine(t, unregisteredClaudeShortName)),
		"schema must reject the short claude spelling no writer emits")

	harp := "u144f05-engine-name-harp"
	writeOneClaudeRecord(t, harp)

	lines := readRecordedRawLines(t, harp)
	require.Len(t, lines, 1)

	var decoded Record
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &decoded))
	assert.Equal(t, registeredClaudeBackendName, decoded.Engine,
		"the recorder passes engine through verbatim; a mismatch here means normalization was added")

	var onDisk interface{}
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &onDisk))
	require.NoError(t, schema.Validate(onDisk),
		"a real claude line must validate against the shipped schema byte-for-byte")
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
