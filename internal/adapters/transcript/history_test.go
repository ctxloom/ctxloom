package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// installFixture copies testdata/fixtures/<name>.transcript.acp.jsonl into
// harp's canonical transcript path (paths.HarpCanonicalTranscriptPath),
// creating the transcripts/ dir as NewRecorder's real writer would, and returns
// the destination path. name is the FIXTURE BASENAME, which is its backend
// registry name (fixtures_test.go's TestFixtures_EngineEnumMatchesManifest
// pins basename == engine field). A name with no file fails as a missing
// FILE, which reads like a broken test rather than a wrong argument.
func installFixture(t *testing.T, name, harp string) string {
	t.Helper()
	src := fixturePath(name + ".transcript.acp.jsonl")
	data, err := os.ReadFile(src)
	require.NoError(t, err, "read fixture %s", name)
	return writeTempTranscript(t, harp, data)
}

// writeTempTranscript writes data to harp's canonical transcript path
// (creating transcripts/ as needed) and returns the path.
func writeTempTranscript(t *testing.T, harp string, data []byte) string {
	t.Helper()
	dst, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, data, 0o644))
	return dst
}

// TestParseTranscriptFile_RoundTrip_RealPayload feeds every capture regime —
// the structured-chat fixtures and a live oneshot record — through
// ParseTranscriptFile end to end (harp -> disk file -> agent.Session)
// and asserts on the reconstructed payload — never merely "no error" or
// "some entries exist". This is the exact discipline the four broken
// per-engine readers skipped (memory "silent-no-op-failure-mode"): PROVE the
// bytes survive the round trip, per regime.
func TestParseTranscriptFile_RoundTrip_RealPayload(t *testing.T) {
	testsupport.Isolate(t)

	t.Run("claude-code", func(t *testing.T) {
		harp := "claude-fixture-harp"
		sess, err := ParseTranscriptFile(installFixture(t, "claude-code", harp), harp)
		require.NoError(t, err)
		require.NotNil(t, sess)
		assert.Equal(t, harp, sess.ID)

		// Span comes from EVERY line's ts (session header through complete
		// footer), not just the entry lines' — proves ParseTranscriptFile
		// doesn't silently narrow to entries-only timing.
		assert.Equal(t, mustParseRFC3339(t, "2026-07-02T15:31:09Z"), sess.StartTime)
		assert.Equal(t, mustParseRFC3339(t, "2026-07-02T15:31:12Z"), sess.EndTime)

		require.Len(t, sess.Entries, 5, "session/complete envelope lines must NOT become entries")
		byType := entriesByType(sess.Entries)
		require.Len(t, byType["user"], 1)
		assert.Contains(t, byType["user"][0].Content, "ping")
		require.Len(t, byType["thinking"], 1)
		assert.NotEmpty(t, byType["thinking"][0].Content)
		require.Len(t, byType["assistant"], 1)
		assert.Equal(t, "ping", byType["assistant"][0].Content)
		require.Len(t, byType["tool_use"], 1)
		assert.Equal(t, "Read", byType["tool_use"][0].ToolName)
		require.Len(t, byType["tool_result"], 1)
		assert.Equal(t, "probe file contents", byType["tool_result"][0].ToolOutput)
	})

	t.Run("mock: permission line is envelope, not conversation", func(t *testing.T) {
		harp := "mock-fixture-harp"
		sess, err := ParseTranscriptFile(installFixture(t, "mock", harp), harp)
		require.NoError(t, err)
		// The denied line (kind=="denied") must NOT surface as a
		// conversation entry — it's a refusal, not turn content — so this
		// fixture's 7 entry-kind lines (user/assistant from the denial turn;
		// user/thinking/tool_use/tool_result/assistant from the TOOLS turn)
		// must be exactly what comes back, proving a KindDenied line is
		// handled without corrupting or dropping its neighbors.
		require.Len(t, sess.Entries, 7)
		byType := entriesByType(sess.Entries)
		require.Len(t, byType["user"], 2)
		assert.Contains(t, byType["user"][0].Content, "mock:deny=")
		require.Len(t, byType["assistant"], 2)
		assert.Equal(t, "mock chat: mock_tool was denied", byType["assistant"][0].Content)
		require.Len(t, byType["thinking"], 1)
		require.Len(t, byType["tool_use"], 1)
		assert.Equal(t, "mock_tool", byType["tool_use"][0].ToolName)
		require.Len(t, byType["tool_result"], 1)
		assert.Contains(t, byType["tool_result"][0].ToolOutput, "mock tool_result")
	})

	t.Run("two-entry oneshot", func(t *testing.T) {
		harp := "mock-oneshot-harp"
		require.NoError(t, RecordOneshot(safefs.New(), harp, "mock", "Reply with just: ok", "ok"))
		path, err := paths.HarpCanonicalTranscriptPath(harp)
		require.NoError(t, err)

		sess, err := ParseTranscriptFile(path, harp)
		require.NoError(t, err)
		require.Len(t, sess.Entries, 2, "oneshot capture is exactly a user entry + an assistant entry")
		assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
		assert.Contains(t, sess.Entries[0].Content, "Reply with just: ok")
		assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[1].Type)
		assert.Equal(t, "ok", sess.Entries[1].Content)
	})
}

// entriesByType buckets entries by their Type string for the "expect exactly
// one of X" assertions above; using a map instead of index-into-slice makes
// each subtest robust to entry re-ordering that isn't semantically load-bearing.
func entriesByType(entries []agent.SessionEntry) map[string][]agent.SessionEntry {
	out := make(map[string][]agent.SessionEntry)
	for _, e := range entries {
		out[string(e.Type)] = append(out[string(e.Type)], e)
	}
	return out
}

func mustParseRFC3339(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	require.NoError(t, err)
	return ts.UTC()
}

// TestParseTranscriptFile_TruncatedLine_DegradesToPartial pins the crash/
// partial contract: a transcript truncated mid-line
// (the process died mid-write) must yield everything readable BEFORE the
// break, never an error for the whole session — matching the same
// degrade-to-partial guarantee agent.SessionStore.ParseSessionFile already
// gives every per-engine reader.
func TestParseTranscriptFile_TruncatedLine_DegradesToPartial(t *testing.T) {
	testsupport.Isolate(t)

	full, err := os.ReadFile(fixturePath("claude-code.transcript.acp.jsonl"))
	require.NoError(t, err)
	lines := splitLines(full)
	require.GreaterOrEqual(t, len(lines), 5)

	// Keep session(0)/user(1)/thinking(2)/assistant(3) intact, then splice in
	// a mid-object fragment of the tool_use line (4) with NO trailing
	// newline — exactly what an interrupted write leaves on disk.
	var truncated []byte
	for i := 0; i < 4; i++ {
		truncated = append(truncated, []byte(lines[i]+"\n")...)
	}
	fragment := lines[4]
	cut := len(fragment) / 2
	truncated = append(truncated, []byte(fragment[:cut])...) // no trailing \n

	dst := writeTempTranscript(t, "claude-truncated-harp", truncated)

	sess, err := ParseTranscriptFile(dst, "claude-truncated-harp")
	require.NoError(t, err, "a truncated trailing line must degrade to partial, not error the whole session")
	require.NotNil(t, sess)
	require.Len(t, sess.Entries, 3, "user+thinking+assistant survive; the truncated tool_use line is dropped")
	byType := entriesByType(sess.Entries)
	assert.Contains(t, byType["user"][0].Content, "ping")
	assert.Equal(t, "ping", byType["assistant"][0].Content)
}

// TestParseTranscriptFile_UnknownSchemaVersion_FailsLoud pins §5's fail-loud
// contract: an unrecognized Record.V must be a hard error for the whole
// file, never a silent guess at an evolved shape.
func TestParseTranscriptFile_UnknownSchemaVersion_FailsLoud(t *testing.T) {
	testsupport.Isolate(t)
	line := `{"v": 99, "harp": "future-harp", "engine": "mock", "seq": 0, "ts": "2026-07-15T00:00:00Z", "kind": "entry", "entry": {"type": "user", "content": "hi"}}` + "\n"
	dst := writeTempTranscript(t, "future-harp", []byte(line))

	_, err := ParseTranscriptFile(dst, "future-harp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version")
}

// splitLines splits fixture bytes into non-empty trimmed lines.
func splitLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			if line := string(data[start:i]); len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		if line := string(data[start:]); len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestParseTranscriptFile_NothingDecoded_FailsLoud pins that three states
// must stay distinguishable at this seam, and before this test only two of
// them were.
//
//  1. A conversation that legitimately carried no ENTRIES (only session/
//     complete envelope lines) → (Session with 0 entries, nil error).
//  2. A file that yielded no RECORDS AT ALL — every line corrupt, or zero
//     bytes — → an ERROR. This is not "an empty conversation"; the writer
//     opens the file lazily on the first SUCCESSFUL Record (see NewRecorder),
//     so a canonical transcript that decodes to nothing is a failed or
//     destroyed capture. Returning (empty, nil) made it indistinguishable
//     from state 1, so a caller that falls back only when GetSession
//     errors got a confidently empty history instead of the transcript that
//     did exist.
//  3. A partially readable file → everything before the break, nil error
//     (already pinned by TestParseTranscriptFile_TruncatedLine_...).
func TestParseTranscriptFile_NothingDecoded_FailsLoud(t *testing.T) {
	testsupport.Isolate(t)

	t.Run("every line corrupt", func(t *testing.T) {
		dst := writeTempTranscript(t, "all-corrupt-harp",
			[]byte("{not json at all\nalso not json\n"))

		sess, err := ParseTranscriptFile(dst, "all-corrupt-harp")
		require.Error(t, err, "a transcript whose every line failed to decode must not report an empty conversation")
		assert.Nil(t, sess)
		assert.Contains(t, err.Error(), "2 line", "the error must say how much was there but unreadable")
	})

	t.Run("zero-byte file", func(t *testing.T) {
		dst := writeTempTranscript(t, "zero-byte-harp", nil)

		sess, err := ParseTranscriptFile(dst, "zero-byte-harp")
		require.Error(t, err, "the writer never creates a zero-byte transcript; one on disk is a failed capture, not an empty chat")
		assert.Nil(t, sess)
	})

	t.Run("envelope-only file is legitimately zero entries", func(t *testing.T) {
		lines := `{"v":1,"harp":"envelope-only-harp","engine":"mock","seq":0,"ts":"2026-07-25T00:00:00Z","kind":"session","session":{"model":"m"}}` + "\n" +
			`{"v":1,"harp":"envelope-only-harp","engine":"mock","seq":1,"ts":"2026-07-25T00:00:01Z","kind":"complete","complete":{"num_turns":1}}` + "\n"
		dst := writeTempTranscript(t, "envelope-only-harp", []byte(lines))

		sess, err := ParseTranscriptFile(dst, "envelope-only-harp")
		require.NoError(t, err, "records decoded fine; they simply carry no entries — that is a real, empty conversation")
		require.NotNil(t, sess)
		assert.Empty(t, sess.Entries)
	})

	t.Run("one good line among corrupt ones still degrades to partial", func(t *testing.T) {
		lines := "{garbage\n" +
			`{"v":1,"harp":"partial-harp","engine":"mock","seq":1,"ts":"2026-07-25T00:00:01Z","kind":"entry","entry":{"type":"user","content":"hi"}}` + "\n" +
			"{more garbage\n"
		dst := writeTempTranscript(t, "partial-harp", []byte(lines))

		sess, err := ParseTranscriptFile(dst, "partial-harp")
		require.NoError(t, err)
		require.Len(t, sess.Entries, 1)
	})
}
