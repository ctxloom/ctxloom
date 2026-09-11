package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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

// allFixtureEngines is DERIVED from the fixture directory, never hand-listed.
// A hand-maintained roster here is an unchecked binding: it silently disagrees
// with the directory the moment a fixture is added or removed, and the failure
// surfaces as a missing FILE, which reads like a broken test rather than a
// stale list. Globbing cannot drift.
//
// Each basename is the fixture's `engine` value, so it must be a registered
// backend name the schema's enum admits (TestFixtures_EngineEnumMatchesManifest
// holds both halves). Provenance per fixture: testdata/fixtures/MANIFEST.json.
var allFixtureEngines = discoverFixtureEngines()

func discoverFixtureEngines() []string {
	const suffix = ".transcript.acp.jsonl"
	matches, err := filepath.Glob(filepath.Join("testdata", "fixtures", "*"+suffix))
	if err != nil {
		panic("glob fixtures: " + err.Error())
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), suffix))
	}
	sort.Strings(names)
	return names
}

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
	t.Run("claude: real ACP ping/pong turn", func(t *testing.T) {
		_, recs := readFixtureLines(t, "claude-code")
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

	t.Run("mock: real forwarded permission request, answered, then a tool turn", func(t *testing.T) {
		_, recs := readFixtureLines(t, "mock")
		var perms []Record
		var sawGranted bool
		for _, r := range recs {
			switch {
			case r.Kind == KindPermission:
				perms = append(perms, r)
			case r.Kind == KindEntry && r.Entry.Type == "assistant" && r.Entry.Content == "mock chat: permission granted":
				sawGranted = true
			}
		}
		require.Len(t, perms, 1, "the mock's PERMISSION turn forwards exactly one request")
		p := perms[0].Permission
		require.NotNil(t, p)
		assert.Equal(t, "mock-perm-1", p.ID)
		assert.Equal(t, "mock_tool", p.ToolName)
		assert.JSONEq(t, `{"action":"scripted"}`, string(p.ToolInput))
		assert.Equal(t, []PermissionOption{
			{ID: "allow", Kind: "allow_once", Name: "Allow"},
			{ID: "reject", Kind: "reject_once", Name: "Reject"},
		}, p.Options)
		assert.True(t, sawGranted, "the answered permission must be followed by the mock's granted reply")

		byType := map[string]int{}
		for _, r := range recs {
			if r.Kind == KindEntry {
				byType[r.Entry.Type]++
			}
		}
		assert.Equal(t, 1, byType["thinking"])
		assert.Equal(t, 1, byType["tool_use"])
		assert.Equal(t, 1, byType["tool_result"])
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

// readFixtureManifest returns the fixture basenames MANIFEST.json documents,
// with the shared suffix stripped so they are comparable to allFixtureEngines.
func readFixtureManifest(t *testing.T) []string {
	t.Helper()
	const suffix = ".transcript.acp.jsonl"

	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "MANIFEST.json"))
	require.NoError(t, err, "read fixture MANIFEST.json")

	var m struct {
		Fixtures map[string]json.RawMessage `json:"fixtures"`
	}
	require.NoError(t, json.Unmarshal(data, &m), "parse fixture MANIFEST.json")

	names := make([]string, 0, len(m.Fixtures))
	for k := range m.Fixtures {
		names = append(names, strings.TrimSuffix(k, suffix))
	}
	return names
}

// TestFixtureRoster_IsNotEmpty guards the derivation itself. allFixtureEngines
// drives three table tests by `for range`; if the glob ever matched nothing —
// a moved directory, a renamed suffix, a test run from the wrong working
// directory — every one of them would iterate zero times and PASS, reporting
// coverage that did not run. That is this project's characteristic silent
// no-op, so the roster is asserted rather than trusted.
func TestFixtureRoster_IsNotEmpty(t *testing.T) {
	require.NotEmpty(t, allFixtureEngines,
		"the fixture glob matched nothing — the schema, seq and manifest tables would all pass vacuously")

	manifest := readFixtureManifest(t)
	assert.ElementsMatch(t, manifest, allFixtureEngines,
		"the fixture directory and MANIFEST.json must describe the same set — a fixture with no manifest entry has undocumented provenance, and a manifest entry with no fixture is a stale claim")
}
