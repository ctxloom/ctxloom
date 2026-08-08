// This suite is self-contained and independently triggerable: it imports
// only internal/transcript (the Recorder sink), internal/shared/agent,
// internal/testsupport (env/HOME isolation), and modernc.org/sqlite (this
// package's own pure-Go driver, for building a fixture db) — no other
// engine's adapter, no other package under internal/transcript/vendorreader. A
// release-monitoring job validating just kiro against a fresh
// data.sqlite3 runs it alone with:
//
//	go test ./internal/transcript/vendorreader/kiro/...
//
// or, from the module root, restricted to this package's own tests:
//
//	go test -run . ./internal/transcript/vendorreader/kiro
//
// or via the dedicated justfile target: `just test-vendor-kiro`.
package kiro

import (
	"bufio"
	"context"
	"database/sql"
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

	_ "modernc.org/sqlite"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

const fixtureHarp = "kiro-fixture-harp"

// repoRoot resolves the module root from this test file's own location
// (internal/transcript/vendorreader/kiro/) so the schema-conformance test can
// reach docs/transcript.schema.json without an embedded copy going stale.
// Mirrors codex/codex_test.go's identical helper exactly.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

// fixtureRow is the shape testdata/conversation-fixture.json is checked in
// as: a human-readable, diffable JSON document standing in for one
// conversations_v2 ROW (key/conversation_id/created_at/updated_at plus the
// row's own `value` JSON blob) — see testdata/MANIFEST.json for why this
// package commits a JSON document and builds its sqlite db AT TEST TIME
// (createFixtureDB below) instead of committing a binary .sqlite3 file the
// way a JSONL-native vendor's fixture would just be checked in directly.
type fixtureRow struct {
	Key            string          `json:"key"`
	ConversationID string          `json:"conversation_id"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
	Value          json.RawMessage `json:"value"`
}

// createFixtureDB builds a fresh sqlite db (conversations_v2 table, per
// schema.go's package doc — exercising the REAL CREATE TABLE/INSERT/SELECT
// round trip through this package's own pure-Go driver, not a mock) seeded
// with the single row loaded from testdata/<jsonFixture>, and returns its
// path plus that row's conversation id.
func createFixtureDB(t *testing.T, jsonFixture string) (dbPath, conversationID string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", jsonFixture))
	require.NoError(t, err)
	var row fixtureRow
	require.NoError(t, json.Unmarshal(raw, &row))

	dbPath = insertFixtureRow(t, row)
	return dbPath, row.ConversationID
}

// mustOpenAndCreateTable creates a fresh sqlite db at dbPath with an empty
// conversations_v2 table (schema.go's package doc's reverse-engineered
// DDL) and returns the open connection — the shared DDL step both
// insertFixtureRow and the empty-db test build on.
func mustOpenAndCreateTable(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open(sqlDriverName, dbPath)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS conversations_v2 (
		key TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		value TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (key, conversation_id)
	)`)
	require.NoError(t, err)
	return db
}

// insertFixtureRow creates a fresh sqlite db at a fresh temp path and
// inserts row into conversations_v2. Split out from createFixtureDB so
// tests needing MULTIPLE rows in one db (e.g. EnumerateConversations) or a
// deliberately malformed row (the degrade-to-partial test) can build one by
// hand without going through the checked-in JSON fixture.
func insertFixtureRow(t *testing.T, row fixtureRow) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "data.sqlite3")
	db := mustOpenAndCreateTable(t, dbPath)
	defer func() { _ = db.Close() }()

	_, err := db.Exec(
		`INSERT INTO conversations_v2 (key, conversation_id, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		row.Key, row.ConversationID, string(row.Value), row.CreatedAt, row.UpdatedAt,
	)
	require.NoError(t, err)
	return dbPath
}

// addFixtureRow inserts an ADDITIONAL row into an existing fixture db (for
// multi-conversation tests like EnumerateConversations).
func addFixtureRow(t *testing.T, dbPath string, row fixtureRow) {
	t.Helper()
	db, err := sql.Open(sqlDriverName, dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	_, err = db.Exec(
		`INSERT INTO conversations_v2 (key, conversation_id, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		row.Key, row.ConversationID, string(row.Value), row.CreatedAt, row.UpdatedAt,
	)
	require.NoError(t, err)
}

// runConvert runs the Adapter against the fixture db at dbPath for
// conversationID into a fresh, isolated Recorder and returns the Records it
// wrote, in the conversation's own turn order. Every TS is zeroed before
// returning — see codex/codex_test.go's runConvert doc comment for why
// (NewRecorder stamps TS from wall-clock time with no injectable clock);
// Seq, by contrast, IS deterministic and asserted verbatim.
func runConvert(t *testing.T, dbPath, conversationID string) []transcript.Record {
	t.Helper()
	testsupport.Isolate(t)

	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)

	err = Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, conversationID))
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
// runConvert reads the Recorder's own output, TS zeroed identically.
func readGolden(t *testing.T) []transcript.Record {
	t.Helper()
	recs := readRecords(t, filepath.Join("testdata", "golden.transcript.acp.jsonl"))
	for i := range recs {
		recs[i].TS = time.Time{}
	}
	return recs
}

// TestConvert_MatchesGolden runs the adapter against the real-shaped
// conversation-fixture.json (see testdata/MANIFEST.json for exactly which
// fields are real vs trimmed) and asserts the canonical output matches
// testdata/golden.transcript.acp.jsonl exactly (TS excepted).
// TestConvert_RealFieldsSurvive below additionally pins specific real
// values by hand so a wrong golden regeneration can't silently mask a real
// regression — same two-test discipline as codex's suite.
func TestConvert_MatchesGolden(t *testing.T) {
	dbPath, conversationID := createFixtureDB(t, "conversation-fixture.json")
	got := runConvert(t, dbPath, conversationID)
	want := readGolden(t)
	require.Equal(t, want, got)
}

// TestConvert_ConformsToJSONSchema validates every produced line against
// docs/transcript.schema.json.
func TestConvert_ConformsToJSONSchema(t *testing.T) {
	testsupport.Isolate(t)
	dbPath, conversationID := createFixtureDB(t, "conversation-fixture.json")

	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, conversationID)))
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
// fixture carries (testdata/MANIFEST.json), never merely "the file parses"
// or "N records exist" — the discipline codex's identically-named test
// documents in full (memory "silent-no-op-failure-mode").
func TestConvert_RealFieldsSurvive(t *testing.T) {
	dbPath, conversationID := createFixtureDB(t, "conversation-fixture.json")
	recs := runConvert(t, dbPath, conversationID)

	var sawSession, sawUser, sawToolUse, sawToolResult, sawSystemNotice, sawAssistant bool
	completeCount := 0
	for _, r := range recs {
		assert.Equal(t, "74b27b3b-19e1-41f3-a8a6-bc8e227a3edb", r.SessionID, "session id must be latched onto every line once known")
		switch r.Kind {
		case transcript.KindSession:
			require.NotNil(t, r.Session)
			assert.Equal(t, "auto", r.Session.Model)
			assert.Equal(t, 1000000, r.Session.ContextWindow)
			assert.Empty(t, r.Session.PermissionMode, "kiro-cli's conversations_v2 row carries no approval-policy analog on this box; must stay an honest gap, never a guess")
			sawSession = true
		case transcript.KindEntry:
			require.NotNil(t, r.Entry)
			switch r.Entry.Type {
			case "user":
				assert.Contains(t, r.Entry.Content, "XXX-OVERWRITTEN-XXX")
				sawUser = true
			case "assistant":
				assert.Contains(t, r.Entry.Content, "blocked by a policy")
				sawAssistant = true
			case "tool_use":
				assert.Equal(t, "fs_write", r.Entry.ToolName)
				assert.Equal(t, "tooluse_RbRZmEcXX5iMZld7EPD5CQ", r.Entry.ToolCallID)
				assert.Contains(t, string(r.Entry.ToolInput), "XXX-OVERWRITTEN-XXX")
				sawToolUse = true
			case "tool_result":
				assert.Equal(t, "tooluse_RbRZmEcXX5iMZld7EPD5CQ", r.Entry.ToolCallID)
				assert.Contains(t, r.Entry.ToolOutput, "Tool use was cancelled by the user")
				assert.True(t, r.Entry.IsError, "kiro's tool_use_results status was 'Error', not 'Success' — must surface as IsError")
				sawToolResult = true
			case "system":
				assert.Contains(t, r.Entry.Content, "rejected because the arguments supplied were forbidden")
				assert.Equal(t, "", r.Entry.SystemKind, "kiro-cli's own rejection notice has no ACP plan structure; SystemKindNotice is the zero value")
				sawSystemNotice = true
			case "thinking":
				t.Fatalf("unexpected thinking entry: kiro fixture has no reasoning content")
			}
		case transcript.KindComplete:
			require.NotNil(t, r.Complete)
			completeCount++
			assert.Equal(t, "auto", r.Complete.Model)
			assert.Equal(t, 0, r.Complete.InputTokens, "every token field is genuinely null on this box's real captures — must default 0 (unknown), never fabricated")
			// The two turns' real durations, per request_metadata's
			// request_start/stream_end timestamps (testdata/MANIFEST.json).
			assert.Contains(t, []int{2828, 2368}, r.Complete.DurationMs, "duration must be the real stream_end-request_start difference, not a placeholder")
		}
	}

	assert.True(t, sawSession, "expected one session record")
	assert.True(t, sawUser, "expected the real user entry")
	assert.True(t, sawToolUse, "expected the real tool_use entry")
	assert.True(t, sawToolResult, "expected the real tool_result entry")
	assert.True(t, sawSystemNotice, "expected the CancelledToolUses rejection notice as a system entry")
	assert.True(t, sawAssistant, "expected the real assistant entry")
	assert.Equal(t, 2, completeCount, "one Complete boundary per history turn — two turns in this fixture")
}

// TestConvert_MalformedTurnDegradesToPartial feeds a two-element history
// array where the first element is syntactically valid JSON but the WRONG
// shape for historyTurn (a bare number, not an object) and the second is a
// well-formed Prompt/Response turn, asserting the first is skipped rather
// than aborting the whole conversion — the same degrade-to-partial contract
// codex's identically-purposed test exercises, adapted to kiro's
// per-history-turn granularity (schema.go's conversationDoc.History doc
// comment: a single JSON document, not independent JSONL lines, so
// "one bad line" becomes "one bad array element" here).
func TestConvert_MalformedTurnDegradesToPartial(t *testing.T) {
	value := `{
		"conversation_id": "partial-convo",
		"history": [
			42,
			{
				"user": {"content": {"Prompt": {"prompt": "still here"}}},
				"assistant": {"Response": {"message_id": "m1", "content": "still answering"}},
				"request_metadata": {"model_id": "test-model", "request_start_timestamp_ms": 100, "stream_end_timestamp_ms": 150}
			}
		],
		"model_info": {"model_id": "test-model", "context_window_tokens": 1000}
	}`
	dbPath := insertFixtureRow(t, fixtureRow{
		Key: "/tmp/partial", ConversationID: "partial-convo",
		CreatedAt: 1, UpdatedAt: 1, Value: json.RawMessage(value),
	})

	recs := runConvert(t, dbPath, "partial-convo")

	var userCount, assistantCount, completeCount int
	for _, r := range recs {
		switch r.Kind {
		case transcript.KindEntry:
			switch r.Entry.Type {
			case "user":
				userCount++
				assert.Equal(t, "still here", r.Entry.Content)
			case "assistant":
				assistantCount++
				assert.Equal(t, "still answering", r.Entry.Content)
			}
		case transcript.KindComplete:
			completeCount++
		}
	}
	assert.Equal(t, 1, userCount, "the malformed first history element must not produce a user entry")
	assert.Equal(t, 1, assistantCount)
	assert.Equal(t, 1, completeCount, "only the well-formed turn contributes a Complete boundary; the malformed element contributes none")
}

// TestConvert_ConversationNotFound asserts a conversation id that is not in
// the (otherwise openable) db is a loud, immediate error — there is no
// partial content to salvage from a row that never existed, distinct from a
// malformed TURN inside a row that does exist.
func TestConvert_ConversationNotFound(t *testing.T) {
	testsupport.Isolate(t)
	dbPath, _ := createFixtureDB(t, "conversation-fixture.json")

	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, "does-not-exist"))
	require.Error(t, err)
}

// TestConvert_OpenFailure asserts a nonexistent db path is a loud, immediate
// error — the "structural failure, no further progress possible" case
// vendorreader.VendorAdapter's doc comment carves out.
func TestConvert_OpenFailure(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	dbPath := filepath.Join(t.TempDir(), "does-not-exist.sqlite3")
	err = Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, "some-id"))
	require.Error(t, err)
}

// TestConvert_BadLocator asserts a src with no composite-locator separator
// is a loud, immediate error rather than being misread as a bare db path
// with an empty conversation id.
func TestConvert_BadLocator(t *testing.T) {
	testsupport.Isolate(t)
	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, "/path/with/no/separator")
	require.Error(t, err)
}

// TestConvert_ContextCancelled asserts an already-cancelled ctx stops the
// conversion instead of running to completion.
func TestConvert_ContextCancelled(t *testing.T) {
	testsupport.Isolate(t)
	dbPath, conversationID := createFixtureDB(t, "conversation-fixture.json")

	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = Adapter{}.Convert(ctx, rec, mustLocator(t, dbPath, conversationID))
	require.ErrorIs(t, err, context.Canceled)
}

// TestConvert_UndecodableDocumentIsFatal covers the one arm of Convert that
// nothing else reached: a conversations_v2 row whose value column is not a
// decodable document at all. Distinct from a malformed TURN, which degrades to
// partial (TestConvert_MalformedTurnDegradesToPartial) — there is no per-turn
// granularity to fall back to when the document itself will not parse, so this
// is the structural, no-further-progress case, and it must name the
// conversation the caller asked for.
func TestConvert_UndecodableDocumentIsFatal(t *testing.T) {
	testsupport.Isolate(t)
	const convID = "conv-undecodable-document"
	dbPath := insertFixtureRow(t, fixtureRow{
		Key: "/some/project", ConversationID: convID,
		CreatedAt: 1, UpdatedAt: 2,
		Value: json.RawMessage(`{"history": [`),
	})

	rec, err := transcript.NewRecorder(fixtureHarp, "kiro")
	require.NoError(t, err)
	defer func() { _ = rec.Close() }()

	err = Adapter{}.Convert(context.Background(), rec, mustLocator(t, dbPath, convID))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode conversation "+convID,
		"an undecodable document must be reported as such, naming the conversation")
}
