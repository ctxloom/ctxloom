// This suite is self-contained and independently triggerable: it reaches for
// no other engine's adapter and nothing under internal/transcript/vendorreader
// beyond the shared helpers this package already builds on, so it depends on
// no other vendor's fixtures or golden files. (Its own support imports —
// internal/transcript's Recorder sink, internal/paths, internal/shared/agent,
// internal/shared/clidiag for warning capture, internal/testsupport for
// env/HOME isolation — are ctxloom-wide infrastructure, not engine
// coupling.) A release-monitoring job
// validating just antigravity against a fresh transcript_full.jsonl runs it
// alone with:
//
//	go test ./internal/transcript/vendorreader/antigravity/...
//
// or, from the module root, restricted to this package's own tests:
//
//	go test -run . ./internal/transcript/vendorreader/antigravity
package antigravity

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

const fixtureHarp = "antigravity-fixture-harp"

// repoRoot resolves the module root from this test file's own location
// (internal/transcript/vendorreader/antigravity/) so the schema-conformance test
// can reach docs/transcript.schema.json without an embedded copy going stale.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

// runConvert runs the Adapter against testdata/<fixture> into a fresh,
// isolated Recorder and returns the Records it wrote, in file order. Every
// TS is zeroed before returning: NewRecorder stamps TS from wall-clock time
// with no injectable clock, so a byte-stable golden comparison must
// normalize it out rather than pin it. seq, by contrast, IS deterministic
// (0..N in the src file's own order) and is asserted verbatim.
func runConvert(t *testing.T, fixture string) []transcript.Record {
	t.Helper()
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)

	src := filepath.Join("testdata", fixture)
	err = Adapter{}.Convert(context.Background(), rec, src)
	require.NoError(t, err)
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	recs := readRecords(t, path)
	for i := range recs {
		recs[i].TS = time.Time{}
	}
	return recs
}

func readRecords(t *testing.T, path string) []transcript.Record {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	var recs []transcript.Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcript.Record
		require.NoError(t, json.Unmarshal([]byte(line), &r), "unmarshal line: %s", line)
		recs = append(recs, r)
	}
	require.NoError(t, scanner.Err())
	return recs
}

// readGolden reads testdata/golden.transcript.acp.jsonl the same way
// runConvert reads the Recorder's own output, with TS zeroed identically so
// the two are comparable regardless of which wall-clock second either ran on.
func readGolden(t *testing.T) []transcript.Record {
	t.Helper()
	recs := readRecords(t, filepath.Join("testdata", "golden.transcript.acp.jsonl"))
	for i := range recs {
		recs[i].TS = time.Time{}
	}
	return recs
}

// TestConvert_MatchesGolden runs the adapter against the real-captured
// transcript_full-fixture.jsonl (see testdata/MANIFEST.json for provenance —
// the whole file is a real, unmodified capture) and asserts the canonical
// output matches testdata/golden.transcript.acp.jsonl exactly (TS excepted —
// see runConvert). This is the full-pipeline fidelity check;
// TestConvert_RealFieldsSurvive below additionally pins specific real values
// by hand so a wrong golden regeneration can't silently mask a real
// regression.
func TestConvert_MatchesGolden(t *testing.T) {
	got := runConvert(t, "transcript_full-fixture.jsonl")
	want := readGolden(t)
	require.Equal(t, want, got)
}

// TestConvert_ConformsToJSONSchema validates every produced line against
// docs/transcript.schema.json — the machine-checkable half of the spec, so a
// structurally-invalid line fails loud here rather than only surfacing when
// some downstream reader chokes on it.
func TestConvert_ConformsToJSONSchema(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, filepath.Join("testdata", "transcript_full-fixture.jsonl")))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	root := repoRoot(t)
	schemaPath := filepath.Join(root, "docs", "transcript.schema.json")
	data, err := os.ReadFile(schemaPath)
	require.NoError(t, err, "read docs/transcript.schema.json")
	compiler := jsonschema.NewCompiler()
	require.NoError(t, compiler.AddResource("transcript.schema.json", strings.NewReader(string(data))))
	schema, err := compiler.Compile("transcript.schema.json")
	require.NoError(t, err, "compile docs/transcript.schema.json")

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNum := 0
	sawLine := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		sawLine = true
		var v interface{}
		require.NoError(t, json.Unmarshal([]byte(line), &v))
		if err := schema.Validate(v); err != nil {
			t.Fatalf("line %d fails schema: %v\nline: %s", lineNum, err, line)
		}
		lineNum++
	}
	require.NoError(t, scanner.Err())
	require.True(t, sawLine, "expected at least one recorded line")
}

// TestConvert_RealFieldsSurvive asserts on the REAL captured content the
// fixture carries (see testdata/MANIFEST.json), never merely "the file
// parses" or "N records exist" — the exact discipline the deleted antigravity
// reader skipped (docs/transcript-schema.md §2b/§8: it keyed a global store
// by an internal uuid the workDir couldn't resolve, so its output was simply
// mis-attributed, never inspected for content).
func TestConvert_RealFieldsSurvive(t *testing.T) {
	recs := runConvert(t, "transcript_full-fixture.jsonl")

	var userCount, assistantCount int
	var sawFirstUser, sawFinalAssistant bool
	for _, r := range recs {
		assert.Equal(t, transcript.KindEntry, r.Kind, "antigravity's native format carries no session/complete-shaped data anywhere the adapter reads; every recorded line must be an entry")
		require.NotNil(t, r.Entry)
		switch r.Entry.Type {
		case "user":
			userCount++
			assert.Equal(t, "Write the exact text XXX-OVERWRITTEN-XXX into the file sentinel.txt, replacing its current contents.", r.Entry.Content, "the <USER_REQUEST> wrapper's ADDITIONAL_METADATA/USER_SETTINGS_CHANGE siblings must be stripped, not recorded as user-authored text")
			assert.NotContains(t, r.Entry.Content, "ADDITIONAL_METADATA", "system-stamped framing must never leak into a canonical user entry")
			sawFirstUser = true
		case "assistant":
			assistantCount++
			if strings.Contains(r.Entry.Content, "I have successfully written") {
				assert.Contains(t, r.Entry.Content, "XXX-OVERWRITTEN-XXX")
				sawFinalAssistant = true
			}
		default:
			t.Fatalf("unexpected entry type %q: antigravity's mapped vocabulary is only user/assistant", r.Entry.Type)
		}
	}

	assert.True(t, sawFirstUser, "expected the real user entry, unwrapped from <USER_REQUEST>")
	assert.True(t, sawFinalAssistant, "expected the real final assistant summary naming the sentinel token")
	assert.Equal(t, 1, userCount, "fixture has exactly one USER_INPUT step")
	assert.Equal(t, 7, assistantCount, "fixture has exactly seven PLANNER_RESPONSE steps")
}

// TestConvert_SkipsUnmappedStepTypesAndNonDoneStatus pins the adapter's
// deliberate omissions against REAL data: the fixture's CONVERSATION_HISTORY,
// GENERIC, CHECKPOINT, LIST_DIRECTORY, SYSTEM_MESSAGE, CODE_ACTION, and
// VIEW_FILE steps must never surface as canonical entries, and its one
// RUNNING-status RUN_COMMAND step must be skipped even though "RUN_COMMAND"
// is not itself a type this adapter would ever map — proving the stepDone
// gate actually runs before the type switch, not that it's dead code shadowed
// by an unmapped type.
func TestConvert_SkipsUnmappedStepTypesAndNonDoneStatus(t *testing.T) {
	recs := runConvert(t, "transcript_full-fixture.jsonl")

	require.Len(t, recs, 8, "1 user + 7 assistant; every other real step in the fixture must be skipped")
	for _, r := range recs {
		assert.NotContains(t, r.Entry.Content, "permission grants", "a GENERIC step's tool-narration content must never surface")
		assert.NotContains(t, r.Entry.Content, "CHECKPOINT", "a CHECKPOINT step's truncation-summary content must never surface")
		assert.NotContains(t, r.Entry.Content, "Empty directory", "a LIST_DIRECTORY step's content must never surface")
		assert.NotContains(t, r.Entry.Content, "background task", "a RUNNING RUN_COMMAND step's content must never surface")
		assert.NotContains(t, r.Entry.Content, "SYSTEM_MESSAGE", "a SYSTEM_MESSAGE step's content must never surface")
	}
}

// TestConvert_MalformedLineDegradesToPartial feeds a three-line file where
// the first line is truncated/corrupt JSON, the second is a step this
// adapter never converts, and the third is a valid user message — asserting
// the corrupt line is skipped and the unmapped step contributes nothing,
// rather than aborting the whole conversion. The same degrade-to-partial
// contract vendorreader.VendorAdapter's doc comment promises.
func TestConvert_MalformedLineDegradesToPartial(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "transcript-partial.jsonl")
	content := "{not valid json\n" +
		`{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE","created_at":"2026-07-16T00:00:00Z"}` + "\n" +
		`{"step_index":2,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-07-16T00:00:00Z","content":"<USER_REQUEST>\nstill here\n</USER_REQUEST>"}` + "\n"
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))

	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, src))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	recs := readRecords(t, path)
	require.Len(t, recs, 1)
	assert.Equal(t, "user", recs[0].Entry.Type)
	assert.Equal(t, "still here", recs[0].Entry.Content)
}

// TestConvert_OpenFailure asserts a nonexistent src is a loud, immediate
// error — the "structural failure, no further progress possible" case
// vendorreader.VendorAdapter's doc comment carves out as the one thing Convert
// must NOT silently swallow.
func TestConvert_OpenFailure(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, filepath.Join("testdata", "does-not-exist.jsonl"))
	require.Error(t, err)
}

// TestConvert_ContextCancelled asserts an already-cancelled ctx stops the
// stream instead of running to completion — the adapter must not ignore
// cancellation on a large/slow import.
func TestConvert_ContextCancelled(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "antigravity")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Adapter{}.Convert(ctx, rec, filepath.Join("testdata", "transcript_full-fixture.jsonl"))
	require.ErrorIs(t, err, context.Canceled)
}
