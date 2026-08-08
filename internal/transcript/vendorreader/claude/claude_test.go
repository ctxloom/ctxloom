// This suite is self-contained and independently triggerable: it imports
// only internal/transcript (the Recorder sink), internal/shared/agent, and
// internal/testsupport (env/HOME isolation) — no other engine's adapter, no
// other package under internal/transcript/vendorreader. A release-monitoring job
// validating just claude against a fresh transcript file runs it alone with:
//
//	go test ./internal/transcript/vendorreader/claude/...
//
// or, from the module root, restricted to this package's own tests:
//
//	go test -run . ./internal/transcript/vendorreader/claude
package claude

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

const fixtureHarp = "claude-fixture-harp"

// repoRoot resolves the module root from this test file's own location
// (internal/transcript/vendorreader/claude/) so the schema-conformance test can
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
// with no injectable clock, so a byte-stable golden comparison must
// normalize it out rather than pin it. seq, by contrast, IS deterministic
// (0..N in the src file's own order) and is asserted verbatim.
func runConvert(t *testing.T, fixture string) []transcript.Record {
	t.Helper()
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
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

// TestConvert_MatchesGolden runs the adapter against the real-shaped
// transcript-fixture.jsonl (see testdata/MANIFEST.json for exactly which
// fields are real vs shortened/synthetic) and asserts the canonical output
// matches testdata/golden.transcript.acp.jsonl exactly (TS excepted — see
// runConvert). This is the full-pipeline fidelity check;
// TestConvert_RealFieldsSurvive below additionally pins specific real values
// by hand so a wrong golden regeneration can't silently mask a real
// regression.
func TestConvert_MatchesGolden(t *testing.T) {
	got := runConvert(t, "transcript-fixture.jsonl")
	want := readGolden(t)
	require.Equal(t, want, got)
}

// TestConvert_ConformsToJSONSchema validates every produced line against
// docs/transcript.schema.json — the machine-checkable half of the spec, so a
// structurally-invalid line fails loud here rather than only surfacing when
// some downstream reader chokes on it.
func TestConvert_ConformsToJSONSchema(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, filepath.Join("testdata", "transcript-fixture.jsonl")))
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
// parses" or "N records exist" — the same discipline codex's own
// TestConvert_RealFieldsSurvive follows, tracing back to the deleted claude
// reader's own failure mode (docs/transcript-schema.md §1/§8): a cwd→slug
// re-encoding bug meant it silently read the WRONG FILE for every real
// session, and nothing caught it until someone actually looked at what came
// out (or didn't).
func TestConvert_RealFieldsSurvive(t *testing.T) {
	recs := runConvert(t, "transcript-fixture.jsonl")

	var sawSession, sawUser, sawThinking, sawAssistant bool
	var sawToolUseGlob, sawToolUseBash, sawToolResultOK, sawToolResultErr, sawInterrupted bool
	var completeCount int
	for _, r := range recs {
		assert.Equal(t, "63147910-550b-4758-b183-91bdd614e267", r.SessionID, "session id must be latched onto every line once known")
		switch r.Kind {
		case transcript.KindSession:
			require.NotNil(t, r.Session)
			assert.Equal(t, "claude-haiku-4-5-20251001", r.Session.Model)
			assert.Equal(t, "default", r.Session.PermissionMode)
			sawSession = true
		case transcript.KindEntry:
			require.NotNil(t, r.Entry)
			switch {
			case r.Entry.Type == "user" && strings.Contains(r.Entry.Content, "Truthfulness audit"):
				sawUser = true
			case r.Entry.Type == "user" && strings.Contains(r.Entry.Content, "[Request interrupted by user for tool use]"):
				sawInterrupted = true
			case r.Entry.Type == "thinking":
				assert.Contains(t, r.Entry.Content, "truthfulness audit")
				sawThinking = true
			case r.Entry.Type == "assistant":
				assert.Contains(t, r.Entry.Content, "trust-gated MCP")
				sawAssistant = true
			case r.Entry.Type == "tool_use" && r.Entry.ToolName == "Glob":
				assert.Equal(t, "toolu_015wVhitD52uyWkG2qUhk2Er", r.Entry.ToolCallID)
				assert.JSONEq(t, `{"pattern":"website/src/content/docs/**/*.md"}`, string(r.Entry.ToolInput))
				sawToolUseGlob = true
			case r.Entry.Type == "tool_use" && r.Entry.ToolName == "Bash":
				assert.Equal(t, "toolu_01ANdQ2y1fgCKwBKn9t6Zcy5", r.Entry.ToolCallID)
				assert.Contains(t, string(r.Entry.ToolInput), "acp-hub-design.plan.md")
				sawToolUseBash = true
			case r.Entry.Type == "tool_result" && r.Entry.ToolCallID == "toolu_015wVhitD52uyWkG2qUhk2Er":
				assert.Contains(t, r.Entry.ToolOutput, "contributing.md")
				assert.False(t, r.Entry.IsError)
				sawToolResultOK = true
			case r.Entry.Type == "tool_result" && r.Entry.ToolCallID == "toolu_01ANdQ2y1fgCKwBKn9t6Zcy5":
				assert.Contains(t, r.Entry.ToolOutput, "tool use was rejected")
				assert.True(t, r.Entry.IsError, "claude's tool_result carries a real is_error field, unlike codex's function_call_output")
				sawToolResultErr = true
			}
		case transcript.KindComplete:
			require.NotNil(t, r.Complete)
			completeCount++
			assert.Equal(t, "claude-haiku-4-5-20251001", r.Complete.Model)
			assert.Equal(t, 10, r.Complete.InputTokens)
			assert.Equal(t, 1, r.Complete.OutputTokens)
			assert.Equal(t, 32371, r.Complete.CacheCreationTokens)
			assert.Equal(t, "", r.Complete.StopReason, "the fixture's session was interrupted mid-turn: stop_reason never arrived")
		}
	}

	assert.True(t, sawSession, "expected one session record")
	assert.True(t, sawUser, "expected the real user entry")
	assert.True(t, sawThinking, "expected the real thinking entry")
	assert.True(t, sawAssistant, "expected the real assistant entry")
	assert.True(t, sawToolUseGlob, "expected the real Glob tool_use entry")
	assert.True(t, sawToolUseBash, "expected the real Bash tool_use entry")
	assert.True(t, sawToolResultOK, "expected the real successful tool_result entry")
	assert.True(t, sawToolResultErr, "expected the real is_error tool_result entry")
	assert.True(t, sawInterrupted, "expected the trailing interrupted-turn user entry")
	assert.Equal(t, 1, completeCount, "one still-open message.id at EOF must flush exactly one Complete boundary")
}

// TestConvert_SkipsSyntheticAndAdminLines pins two of the adapter's
// deliberate omissions (session.go's messageEntries/line doc comments): an
// isMeta:true "user" line (claude's own injected <local-command-caveat>
// wrapper) never becomes a canonical entry, and non-conversational line
// types (queue-operation, progress, system) contribute nothing at all. The
// fixture has exactly one isMeta:true line and three admin-type lines; a
// leak of either omission would surface as an unexpected extra entry.
func TestConvert_SkipsSyntheticAndAdminLines(t *testing.T) {
	recs := runConvert(t, "transcript-fixture.jsonl")

	for _, r := range recs {
		if r.Kind != transcript.KindEntry {
			continue
		}
		assert.NotContains(t, r.Entry.Content, "local-command-caveat", "an isMeta:true user line must never surface as a canonical entry")
	}
	// 1 session + 8 real entries (user, thinking, assistant, tool_use x2,
	// tool_result x2, interrupted-user) + 1 complete = 10. A leaked
	// isMeta/admin line would inflate this count.
	assert.Len(t, recs, 10)
}

// TestConvert_TurnBoundaryOnMessageIDChange feeds turn-boundary-fixture.jsonl
// (real assistant lines from a DIFFERENT captured session — see
// testdata/MANIFEST.json — spanning two distinct message.id groups, the
// first with stop_reason "tool_use" and the second "end_turn") and asserts
// TWO Complete boundaries are recorded: one flushed mid-stream when the
// second message.id begins, and one flushed at end of file. This is the
// turn-accounting path TestConvert_RealFieldsSurvive's interrupted-session
// fixture cannot exercise, since every assistant line there shares the SAME
// message.id.
func TestConvert_TurnBoundaryOnMessageIDChange(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, filepath.Join("testdata", "turn-boundary-fixture.jsonl")))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	recs := readRecords(t, path)

	var completes []*transcript.CompletePayload
	for _, r := range recs {
		if r.Kind == transcript.KindComplete {
			completes = append(completes, r.Complete)
		}
	}
	require.Len(t, completes, 2, "two distinct message.id groups must yield two Complete boundaries, not one merged or four per-line")

	assert.Equal(t, 17479, completes[0].InputTokens)
	assert.Equal(t, 216, completes[0].OutputTokens)
	assert.Equal(t, 15201, completes[0].CacheReadTokens)
	assert.Equal(t, "tool_use", completes[0].StopReason)

	assert.Equal(t, 2, completes[1].InputTokens)
	assert.Equal(t, 393, completes[1].OutputTokens)
	assert.Equal(t, 48290, completes[1].CacheReadTokens)
	assert.Equal(t, "end_turn", completes[1].StopReason)
}

// TestConvert_MalformedLineDegradesToPartial feeds a two-line file where the
// first line is truncated/corrupt JSON and the second is a valid user
// message, asserting the corrupt line is skipped rather than aborting the
// whole conversion — the same degrade-to-partial contract
// vendorreader.VendorAdapter's doc comment promises.
func TestConvert_MalformedLineDegradesToPartial(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "transcript-partial.jsonl")
	content := "{not valid json\n" +
		`{"type":"user","sessionId":"partial-session","message":{"role":"user","content":"still here"}}` + "\n"
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, src))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	recs := readRecords(t, path)
	require.Len(t, recs, 2, "the malformed line contributes no session metadata either, but a valid sessionId still latches a Session record ahead of the one real entry")
	assert.Equal(t, transcript.KindSession, recs[0].Kind)
	assert.Equal(t, transcript.KindEntry, recs[1].Kind)
	assert.Equal(t, "user", recs[1].Entry.Type)
	assert.Equal(t, "still here", recs[1].Entry.Content)
}

// TestConvert_OpenFailure asserts a nonexistent src is a loud, immediate
// error — the "structural failure, no further progress possible" case
// vendorreader.VendorAdapter's doc comment carves out as the one thing Convert
// must NOT silently swallow.
func TestConvert_OpenFailure(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
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
	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Adapter{}.Convert(ctx, rec, filepath.Join("testdata", "transcript-fixture.jsonl"))
	require.ErrorIs(t, err, context.Canceled)
}
