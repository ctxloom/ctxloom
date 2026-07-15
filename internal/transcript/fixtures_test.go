package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// repoRoot resolves the ctxloom module root from this test file's location
// (internal/transcript/) so the fixture tests can reach docs/ without an
// embedded copy going stale relative to the one true schema file.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// internal/transcript/fixtures_test.go -> repo root is two levels up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func compileTranscriptSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	root := repoRoot(t)
	schemaPath := filepath.Join(root, "docs", "transcript.schema.json")
	data, err := os.ReadFile(schemaPath)
	require.NoError(t, err, "read docs/transcript.schema.json")

	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("transcript.schema.json", strings.NewReader(string(data))))
	schema, err := compiler.Compile("transcript.schema.json")
	require.NoError(t, err, "compile docs/transcript.schema.json")
	return schema
}

// readFixtureLines reads a testdata fixture and returns both the raw JSON
// lines (for schema validation, which wants interface{}) and the decoded
// Records (for payload assertions).
func readFixtureLines(t *testing.T, engine string) ([]string, []Record) {
	t.Helper()
	path := filepath.Join("testdata", "fixtures", engine+".transcript.acp.jsonl")
	f, err := os.Open(path)
	require.NoError(t, err, "open fixture for %s", engine)
	defer f.Close()

	var raw []string
	var recs []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		raw = append(raw, line)
		var r Record
		require.NoError(t, json.Unmarshal([]byte(line), &r), "unmarshal %s line: %s", engine, line)
		recs = append(recs, r)
	}
	require.NoError(t, scanner.Err())
	require.NotEmpty(t, raw, "fixture for %s must not be empty", engine)
	return raw, recs
}

var allFixtureEngines = []string{"codex", "kiro", "claude", "opencode", "acp", "antigravity"}

// TestFixtures_ConformToJSONSchema validates every line of every per-engine
// fixture against docs/transcript.schema.json — the machine-checkable half of
// the spec, kept honest against real (or realistic) data, not just against
// whatever the Recorder itself happens to emit.
func TestFixtures_ConformToJSONSchema(t *testing.T) {
	schema := compileTranscriptSchema(t)
	for _, engine := range allFixtureEngines {
		t.Run(engine, func(t *testing.T) {
			raw, _ := readFixtureLines(t, engine)
			for i, line := range raw {
				var v interface{}
				require.NoError(t, json.Unmarshal([]byte(line), &v))
				if err := schema.Validate(v); err != nil {
					t.Fatalf("%s line %d fails schema: %v\nline: %s", engine, i, err, line)
				}
			}
		})
	}
}

// TestFixtures_SeqIsMonotonicGapFree pins the ordering contract on every
// fixture, real or synthetic: seq starts at 0 and increases by exactly 1.
func TestFixtures_SeqIsMonotonicGapFree(t *testing.T) {
	for _, engine := range allFixtureEngines {
		t.Run(engine, func(t *testing.T) {
			_, recs := readFixtureLines(t, engine)
			for i, r := range recs {
				assert.Equal(t, i, r.Seq, "%s: seq must be gap-free starting at 0", engine)
			}
		})
	}
}

// TestFixtures_RealPayloadSurvives asserts on the REAL captured content each
// fixture carries (per testdata/fixtures/MANIFEST.json) — never merely "the
// file parses" or "entry count > 0". This is the exact discipline the
// project's four broken engine readers skipped (memory
// "silent-no-op-failure-mode" / "Mutation gate truthfulness"): a reader that
// silently drops or mangles the payload must fail a test, not slip through on
// a shape-only check.
func TestFixtures_RealPayloadSurvives(t *testing.T) {
	t.Run("codex: real session id, model, token accounting, and the literal echoed token", func(t *testing.T) {
		_, recs := readFixtureLines(t, "codex")
		require.NotEmpty(t, recs)
		for _, r := range recs {
			assert.Equal(t, "019f6226-e5d2-75f3-b8bb-667866092679", r.SessionID)
		}
		var sawUser, sawAssistant, sawComplete bool
		for _, r := range recs {
			switch {
			case r.Kind == KindEntry && r.Entry.Type == "user":
				assert.Contains(t, r.Entry.Content, "RAWNONCE-7Q4Z")
				sawUser = true
			case r.Kind == KindEntry && r.Entry.Type == "assistant":
				assert.Equal(t, "RAWNONCE-7Q4Z", r.Entry.Content)
				sawAssistant = true
			case r.Kind == KindComplete:
				assert.Equal(t, 20240, r.Complete.InputTokens)
				assert.Equal(t, 4480, r.Complete.CacheReadTokens)
				assert.Equal(t, 27, r.Complete.OutputTokens)
				assert.Equal(t, 258400, r.Complete.ContextWindow)
				assert.Equal(t, "gpt-5.4-mini", r.Complete.Model)
				sawComplete = true
			}
		}
		assert.True(t, sawUser, "expected a real user entry")
		assert.True(t, sawAssistant, "expected a real assistant entry")
		assert.True(t, sawComplete, "expected a real complete accounting line")
	})

	t.Run("kiro: real tool-calling turn round-trips file path, output, and final answer", func(t *testing.T) {
		_, recs := readFixtureLines(t, "kiro")
		var sawToolUse, sawToolResult, sawFinalAssistant bool
		for _, r := range recs {
			if r.Kind != KindEntry {
				continue
			}
			switch r.Entry.Type {
			case "tool_use":
				assert.Equal(t, "read", r.Entry.ToolName)
				assert.Contains(t, string(r.Entry.ToolInput), "probe6.txt")
				sawToolUse = true
			case "tool_result":
				assert.Equal(t, "sixth probe file contents for tool-call capture", r.Entry.ToolOutput)
				assert.False(t, r.Entry.IsError)
				sawToolResult = true
			case "assistant":
				if strings.Contains(r.Entry.Content, "sixth probe file contents for tool-call capture") {
					sawFinalAssistant = true
				}
			}
		}
		assert.True(t, sawToolUse, "expected the real tool_use entry")
		assert.True(t, sawToolResult, "expected the real tool_result entry")
		assert.True(t, sawFinalAssistant, "expected the assistant's final answer to quote the real tool output")
	})

	t.Run("claude: real ACP ping/pong turn", func(t *testing.T) {
		_, recs := readFixtureLines(t, "claude")
		var sawUser, sawAssistant bool
		for _, r := range recs {
			if r.Kind != KindEntry {
				continue
			}
			if r.Entry.Type == "user" && strings.Contains(r.Entry.Content, "ping") {
				sawUser = true
			}
			if r.Entry.Type == "assistant" && r.Entry.Content == "ping" {
				sawAssistant = true
			}
		}
		assert.True(t, sawUser)
		assert.True(t, sawAssistant)
	})

	t.Run("antigravity: real oneshot two-entry capture", func(t *testing.T) {
		_, recs := readFixtureLines(t, "antigravity")
		require.Len(t, recs, 2, "oneshot capture is exactly a user entry + an assistant entry")
		assert.Equal(t, "user", recs[0].Entry.Type)
		assert.Contains(t, recs[0].Entry.Content, "Reply with just: ok")
		assert.Equal(t, "assistant", recs[1].Entry.Type)
		assert.Equal(t, "ok", recs[1].Entry.Content)
	})
}

// TestFixtures_EngineEnumMatchesManifest is a light drift guard: every
// fixture file name must be a real engine value the schema's enum allows.
func TestFixtures_EngineEnumMatchesManifest(t *testing.T) {
	schema := compileTranscriptSchema(t)
	for _, engine := range allFixtureEngines {
		raw, _ := readFixtureLines(t, engine)
		var v map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(raw[0]), &v))
		assert.Equal(t, engine, v["engine"])
		require.NoError(t, schema.Validate(v))
	}
}
