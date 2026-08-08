// This suite is self-contained and independently triggerable: it reaches for
// no other engine's adapter and nothing else under
// internal/transcript/vendorreader, so it can be run alone, from the module root,
// with either of:
//
//	go test ./internal/transcript/vendorreader/codex/...
//	go test -run . ./internal/transcript/vendorreader/codex
//
// What those commands validate is the FROZEN fixture checked in under
// testdata/ — rollout-fixture.jsonl, captured from a real codex session on
// 2026-07-16 (provenance in testdata/MANIFEST.json) — not a fresh capture.
// Running them proves this build still parses that recorded shape correctly;
// it proves nothing about a codex release that shipped afterwards.
//
// Pointing this suite at a NEWER codex build is a two-step manual job, and
// deliberately so: replace testdata/rollout-fixture.jsonl with the new
// capture, update testdata/MANIFEST.json's provenance to match, then
// regenerate testdata/golden.transcript.acp.jsonl from the adapter's own
// output. The regenerated golden cannot by itself catch a regression it was
// generated through, which is why TestConvert_RealFieldsSurvive pins real
// captured values by hand alongside it.
package codex

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

const fixtureHarp = "codex-fixture-harp"

// repoRoot resolves the module root from this test file's own location
// (internal/transcript/vendorreader/codex/) so the schema-conformance test can
// reach docs/transcript.schema.json without an embedded copy going stale.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

// runConvert runs the Adapter against testdata/<fixture> into a fresh,
// isolated Recorder and returns the Records it wrote, in file order. Every
// TS is zeroed before returning: NewRecorder stamps TS from wall-clock time
// with no injectable clock (recorder.go's fileRecorder.now is not
// caller-configurable), so a byte-stable golden comparison must normalize it
// out rather than pin it. seq, by contrast, IS deterministic (0..N in the
// src file's own order) and is asserted verbatim.
func runConvert(t *testing.T, fixture string) []transcript.Record {
	t.Helper()
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
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
// the two are comparable regardless of which wall-clock second either ran
// on.
func readGolden(t *testing.T) []transcript.Record {
	t.Helper()
	recs := readRecords(t, filepath.Join("testdata", "golden.transcript.acp.jsonl"))
	for i := range recs {
		recs[i].TS = time.Time{}
	}
	return recs
}

// TestConvert_MatchesGolden runs the adapter against the real-shaped
// rollout-fixture.jsonl (see testdata/MANIFEST.json for exactly which fields
// are real vs shortened) and asserts the canonical output matches
// testdata/golden.transcript.acp.jsonl exactly (TS excepted — see runConvert).
// This is the full-pipeline fidelity check; TestConvert_RealFieldsSurvive
// below additionally pins specific real values by hand so a wrong golden
// regeneration can't silently mask a real regression.
func TestConvert_MatchesGolden(t *testing.T) {
	got := runConvert(t, "rollout-fixture.jsonl")
	want := readGolden(t)
	require.Equal(t, want, got)
}

// TestConvert_ConformsToJSONSchema validates every produced line against
// docs/transcript.schema.json — the machine-checkable half of the spec, so a
// structurally-invalid line fails loud here rather than only surfacing when
// some downstream reader chokes on it.
func TestConvert_ConformsToJSONSchema(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, filepath.Join("testdata", "rollout-fixture.jsonl")))
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
// parses" or "N records exist" — the exact discipline the deleted codex
// reader skipped (docs/transcript-schema.md §1/§8): it silently returned
// zero-entry sessions for every real rollout file, and nothing caught it
// until someone actually looked at what came out.
func TestConvert_RealFieldsSurvive(t *testing.T) {
	recs := runConvert(t, "rollout-fixture.jsonl")

	var sawSession, sawUser, sawToolUse, sawToolResult, sawAssistant bool
	var completeCount int
	for _, r := range recs {
		assert.Equal(t, "019f6c7c-18d6-7d63-a2e7-f8da2d1aeedf", r.SessionID, "session id must be latched onto every line once known")
		switch r.Kind {
		case transcript.KindSession:
			require.NotNil(t, r.Session)
			assert.Equal(t, "gpt-5.5", r.Session.Model)
			assert.Equal(t, "on-request", r.Session.PermissionMode)
			assert.Equal(t, 258400, r.Session.ContextWindow)
			sawSession = true
		case transcript.KindEntry:
			require.NotNil(t, r.Entry)
			switch r.Entry.Type {
			case "user":
				assert.Contains(t, r.Entry.Content, "HELLO_FROM_TOOL_CALL_42")
				sawUser = true
			case "assistant":
				assert.Contains(t, r.Entry.Content, "HELLO_FROM_TOOL_CALL_42")
				sawAssistant = true
			case "tool_use":
				assert.Equal(t, "exec_command", r.Entry.ToolName)
				assert.Equal(t, "call_OfxrXuWLSfik7ZgLtgbOViU6", r.Entry.ToolCallID)
				// Decoded, not substring-matched. codex's `arguments` is a
				// JSON-ENCODED STRING that argumentsToRaw must unwrap into an
				// object; its defensive fallback instead wraps the whole thing
				// as a JSON string literal. A Contains check cannot tell those
				// two apart — the expected text is present either way — so it
				// passed against a ToolInput that had silently become a quoted
				// blob no consumer could read as arguments. This is also the
				// one property whose only other guard is a golden file the
				// adapter itself regenerates, which is precisely what this
				// hand-written backstop exists to be independent of.
				var toolInput map[string]any
				require.NoError(t, json.Unmarshal(r.Entry.ToolInput, &toolInput),
					"arguments must reach ToolInput as a JSON OBJECT, not as an escaped string literal: %s", r.Entry.ToolInput)
				assert.Equal(t, "echo HELLO_FROM_TOOL_CALL_42", toolInput["cmd"])
				assert.Equal(t, "/tmp/ctxloom-acp-verify", toolInput["workdir"])
				sawToolUse = true
			case "tool_result":
				assert.Equal(t, "call_OfxrXuWLSfik7ZgLtgbOViU6", r.Entry.ToolCallID)
				assert.Contains(t, r.Entry.ToolOutput, "Process exited with code 0")
				assert.Contains(t, r.Entry.ToolOutput, "HELLO_FROM_TOOL_CALL_42")
				assert.False(t, r.Entry.IsError, "codex's function_call_output carries no error field on this build; must default false, never guessed true")
				sawToolResult = true
			case "system", "thinking":
				t.Fatalf("unexpected %s entry: codex fixture has no plan/reasoning content", r.Entry.Type)
			}
		case transcript.KindComplete:
			require.NotNil(t, r.Complete)
			completeCount++
			assert.Equal(t, 258400, r.Complete.ContextWindow)
			// Second (final) turn's per-turn accounting, per
			// tokenCountPayload's doc comment: last_token_usage, not the
			// cumulative total_token_usage.
			if r.Complete.InputTokens == 29010 {
				assert.Equal(t, 28544, r.Complete.CacheReadTokens)
				assert.Equal(t, 19, r.Complete.OutputTokens)
				assert.Equal(t, 7085, r.Complete.DurationMs, "duration only arrives on the task_complete that closes the boundary")
			}
		}
	}

	assert.True(t, sawSession, "expected one session record")
	assert.True(t, sawUser, "expected the real user entry")
	assert.True(t, sawToolUse, "expected the real tool_use entry")
	assert.True(t, sawToolResult, "expected the real tool_result entry")
	assert.True(t, sawAssistant, "expected the real assistant entry")
	assert.Equal(t, 1, completeCount, "codex's two token_count events for this fixture both fold into the SAME task_complete boundary, not two separate Complete lines")
}

// TestConvert_SkipsDeveloperRoleAndEventMsgEchoes pins two of the adapter's
// deliberate omissions (rollout.go's messageEvents/handleEventMsg doc
// comments): a developer-role response_item never becomes a canonical entry,
// and event_msg's user_message/agent_message notifications never duplicate
// the response_item entries that already carry the same content. The fixture
// has exactly one real user turn and one real assistant turn; a leak of
// either omission would inflate those counts.
func TestConvert_SkipsDeveloperRoleAndEventMsgEchoes(t *testing.T) {
	recs := runConvert(t, "rollout-fixture.jsonl")

	var userCount, assistantCount int
	for _, r := range recs {
		if r.Kind != transcript.KindEntry {
			continue
		}
		switch r.Entry.Type {
		case "user":
			userCount++
		case "assistant":
			assistantCount++
		}
		assert.NotContains(t, r.Entry.Content, "sandbox_mode", "a developer-role response_item must never surface as a canonical entry")
	}
	assert.Equal(t, 1, userCount, "one response_item role=user must yield exactly one entry, not duplicated by event_msg.user_message")
	assert.Equal(t, 1, assistantCount, "one response_item role=assistant must yield exactly one entry, not duplicated by event_msg.agent_message")
}

// TestConvert_MalformedLineDegradesToPartial feeds a two-line file where the
// first line is truncated/corrupt JSON and the second is a valid user
// message, asserting the corrupt line is skipped rather than aborting the
// whole conversion — the same degrade-to-partial contract
// vendorreader.VendorAdapter's doc comment promises, and the one property that
// distinguishes an honest vendor reader from the deleted codex scraper (which
// failed differently, but just as silently, by decoding into the wrong
// shape and returning nothing at all).
func TestConvert_MalformedLineDegradesToPartial(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "rollout-partial.jsonl")
	content := "{not valid json\n" +
		`{"timestamp":"2026-07-16T00:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"still here"}]}}` + "\n"
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))

	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
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
	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
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
	rec, err := transcript.NewRecorder(fixtureHarp, "codex")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Adapter{}.Convert(ctx, rec, filepath.Join("testdata", "rollout-fixture.jsonl"))
	require.ErrorIs(t, err, context.Canceled)
}
