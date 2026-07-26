package transcript

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// installFixture copies a testdata fixture verbatim to harp's canonical
// transcript path (paths.HarpCanonicalTranscriptPath), creating the persist/
// dir as NewRecorder's real writer would. Returns the destination path.
func installFixture(t *testing.T, engine, harp string) string {
	t.Helper()
	src := filepath.Join("testdata", "fixtures", engine+".transcript.acp.jsonl")
	data, err := os.ReadFile(src)
	require.NoError(t, err, "read fixture for %s", engine)
	return writeTempTranscript(t, harp, data)
}

// writeTempTranscript writes data to harp's canonical transcript path
// (creating persist/ as needed) and returns the path.
func writeTempTranscript(t *testing.T, harp string, data []byte) string {
	t.Helper()
	dst, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, data, 0o644))
	return dst
}

// mint registers harp under projectDir in store (AssignHarp mints its own
// name; Rename retargets it to the exact name a fixture was installed
// under — the only way MemStore's Store-port surface lets a test pick a
// specific harp name).
func mint(t *testing.T, store *sessions.MemStore, harp, projectDir string) {
	t.Helper()
	e, err := store.AssignHarp(projectDir, "test-engine")
	require.NoError(t, err)
	require.NoError(t, store.Rename(e.HarpName, harp))
}

// TestCanonicalHistory_RoundTrip_RealPayload feeds each of S1's six engine
// fixtures through CanonicalHistory.GetSession end to end (harp -> disk file
// -> agent.Session) and asserts on the reconstructed payload — never merely
// "no error" or "some entries exist". This is the exact discipline the four
// broken per-engine readers skipped (memory "silent-no-op-failure-mode"):
// PROVE the bytes survive the round trip, per engine.
func TestCanonicalHistory_RoundTrip_RealPayload(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()

	t.Run("codex", func(t *testing.T) {
		harp := "codex-fixture-harp"
		installFixture(t, "codex", harp)
		h := NewCanonicalHistory("/proj/codex", sessions.NewMemStore())

		sess, err := h.GetSession(ctx, harp)
		require.NoError(t, err)
		require.NotNil(t, sess)
		assert.Equal(t, harp, sess.ID)

		// Span comes from EVERY line's ts (session header through complete
		// footer), not just the entry lines' — proves ParseTranscriptFile
		// doesn't silently narrow to entries-only timing.
		assert.Equal(t, mustParseRFC3339(t, "2026-07-14T19:42:24Z"), sess.StartTime)
		assert.Equal(t, mustParseRFC3339(t, "2026-07-14T19:42:39Z"), sess.EndTime)

		require.Len(t, sess.Entries, 5, "session/complete envelope lines must NOT become entries")
		byType := entriesByType(sess.Entries)
		require.Len(t, byType["user"], 1)
		assert.Contains(t, byType["user"][0].Content, "RAWNONCE-7Q4Z")
		require.Len(t, byType["thinking"], 1)
		assert.NotEmpty(t, byType["thinking"][0].Content)
		require.Len(t, byType["assistant"], 1)
		assert.Equal(t, "RAWNONCE-7Q4Z", byType["assistant"][0].Content)
		require.Len(t, byType["tool_use"], 1)
		assert.Equal(t, "shell", byType["tool_use"][0].ToolName)
		assert.Contains(t, string(byType["tool_use"][0].ToolInput), "probe.txt")
		require.Len(t, byType["tool_result"], 1)
		assert.Equal(t, "probe file contents", byType["tool_result"][0].ToolOutput)
		assert.False(t, byType["tool_result"][0].IsError)
	})

	t.Run("kiro", func(t *testing.T) {
		harp := "kiro-fixture-harp"
		installFixture(t, "kiro", harp)
		h := NewCanonicalHistory("/proj/kiro", sessions.NewMemStore())

		sess, err := h.GetSession(ctx, harp)
		require.NoError(t, err)
		require.Len(t, sess.Entries, 4)

		byType := entriesByType(sess.Entries)
		require.Len(t, byType["tool_use"], 1)
		assert.Equal(t, "read", byType["tool_use"][0].ToolName)
		assert.Contains(t, string(byType["tool_use"][0].ToolInput), "probe6.txt")
		require.Len(t, byType["tool_result"], 1)
		assert.Equal(t, "sixth probe file contents for tool-call capture", byType["tool_result"][0].ToolOutput)
		assert.False(t, byType["tool_result"][0].IsError)
		require.Len(t, byType["assistant"], 1)
		assert.Contains(t, byType["assistant"][0].Content, "sixth probe file contents for tool-call capture")
	})

	t.Run("claude", func(t *testing.T) {
		harp := "claude-fixture-harp"
		installFixture(t, "claude", harp)
		h := NewCanonicalHistory("/proj/claude", sessions.NewMemStore())

		sess, err := h.GetSession(ctx, harp)
		require.NoError(t, err)
		require.Len(t, sess.Entries, 5)

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

	t.Run("opencode", func(t *testing.T) {
		harp := "opencode-fixture-harp"
		installFixture(t, "opencode", harp)
		h := NewCanonicalHistory("/proj/opencode", sessions.NewMemStore())

		sess, err := h.GetSession(ctx, harp)
		require.NoError(t, err)
		require.Len(t, sess.Entries, 5)

		byType := entriesByType(sess.Entries)
		require.Len(t, byType["user"], 1)
		assert.Contains(t, byType["user"][0].Content, "What does this function do?")
		require.Len(t, byType["tool_use"], 1)
		assert.Equal(t, "read", byType["tool_use"][0].ToolName)
		assert.Contains(t, string(byType["tool_use"][0].ToolInput), "main.go")
		require.Len(t, byType["tool_result"], 1)
		assert.Equal(t, "func main() { ... }", byType["tool_result"][0].ToolOutput)
		require.Len(t, byType["assistant"], 1)
		assert.Equal(t, "It's the program's entry point.", byType["assistant"][0].Content)
	})

	t.Run("acp", func(t *testing.T) {
		harp := "acp-fixture-harp"
		installFixture(t, "acp", harp)
		h := NewCanonicalHistory("/proj/acp", sessions.NewMemStore())

		sess, err := h.GetSession(ctx, harp)
		require.NoError(t, err)
		// The permission line (kind=="permission") must NOT surface as a
		// conversation entry — it's a forwarded request, not turn content —
		// so this fixture's 4 entry-kind lines (user/tool_use/tool_result/
		// assistant) must be exactly what comes back, proving a KindPermission
		// line is handled without corrupting or dropping its neighbors.
		require.Len(t, sess.Entries, 4)
		byType := entriesByType(sess.Entries)
		require.Len(t, byType["user"], 1)
		assert.Contains(t, byType["user"][0].Content, "Delete the scratch directory")
		require.Len(t, byType["tool_use"], 1)
		assert.Equal(t, "rm", byType["tool_use"][0].ToolName)
		require.Len(t, byType["tool_result"], 1)
		assert.Equal(t, "removed", byType["tool_result"][0].ToolOutput)
		require.Len(t, byType["assistant"], 1)
		assert.Contains(t, byType["assistant"][0].Content, "/tmp/scratch is gone")
	})

	t.Run("antigravity", func(t *testing.T) {
		harp := "antigravity-fixture-harp"
		installFixture(t, "antigravity", harp)
		h := NewCanonicalHistory("/proj/antigravity", sessions.NewMemStore())

		sess, err := h.GetSession(ctx, harp)
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

// TestCanonicalHistory_GetSession_NoFile_Errors pins the read-side mirror of
// the Recorder's empty-input contract (recorder_test.go's
// TestRecorder_EmptyInput_WritesNoFile): a harp with no captured transcript
// must fail loud, not return an empty-but-successful Session that could be
// mistaken for "captured, and genuinely empty".
func TestCanonicalHistory_GetSession_NoFile_Errors(t *testing.T) {
	testsupport.Isolate(t)
	h := NewCanonicalHistory("/proj/none", sessions.NewMemStore())

	sess, err := h.GetSession(context.Background(), "never-captured-harp")
	assert.Error(t, err)
	assert.Nil(t, sess)
}

// TestCanonicalHistory_GetSession_EmptyHarp_Errors pins fail-loud-on-misuse:
// a blank harp is a caller bug, rejected immediately.
func TestCanonicalHistory_GetSession_EmptyHarp_Errors(t *testing.T) {
	testsupport.Isolate(t)
	h := NewCanonicalHistory("/proj/none", sessions.NewMemStore())

	_, err := h.GetSession(context.Background(), "")
	assert.Error(t, err)
}

// TestCanonicalHistory_GetSession_FallsBackToLegacyFilename pins the
// transcript.acp.jsonl -> transcript.jsonl rename's back-compat contract: a
// harp captured before the rename has ONLY the legacy filename on disk (no
// writer ever produced the current-name file for it), and GetSession must
// still resolve and parse it via paths.ResolveHarpCanonicalTranscriptPath —
// not report "no canonical transcript captured" for a session that
// genuinely has one.
func TestCanonicalHistory_GetSession_FallsBackToLegacyFilename(t *testing.T) {
	testsupport.Isolate(t)
	harp := "pre-rename-harp"

	dir, err := paths.HarpPersistDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "codex.transcript.acp.jsonl"))
	require.NoError(t, err)
	legacyPath := filepath.Join(dir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, data, 0o644))

	h := NewCanonicalHistory("/proj/legacy", sessions.NewMemStore())
	sess, err := h.GetSession(context.Background(), harp)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, harp, sess.ID)
	assert.NotEmpty(t, sess.Entries, "the legacy-named file's real content must survive the fallback read")
}

// TestParseTranscriptFile_TruncatedLine_DegradesToPartial pins the crash/
// partial contract (tough-cloud plan §6.4): a transcript truncated mid-line
// (the process died mid-write) must yield everything readable BEFORE the
// break, never an error for the whole session — matching the same
// degrade-to-partial guarantee agent.SessionStore.ParseSessionFile already
// gives every per-engine reader.
func TestParseTranscriptFile_TruncatedLine_DegradesToPartial(t *testing.T) {
	testsupport.Isolate(t)

	full, err := os.ReadFile(filepath.Join("testdata", "fixtures", "codex.transcript.acp.jsonl"))
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

	dst := writeTempTranscript(t, "codex-truncated-harp", truncated)

	sess, err := ParseTranscriptFile(dst, "codex-truncated-harp")
	require.NoError(t, err, "a truncated trailing line must degrade to partial, not error the whole session")
	require.NotNil(t, sess)
	require.Len(t, sess.Entries, 3, "user+thinking+assistant survive; the truncated tool_use line is dropped")
	byType := entriesByType(sess.Entries)
	assert.Contains(t, byType["user"][0].Content, "RAWNONCE-7Q4Z")
	assert.Equal(t, "RAWNONCE-7Q4Z", byType["assistant"][0].Content)
}

// TestParseTranscriptFile_UnknownSchemaVersion_FailsLoud pins §5's fail-loud
// contract: an unrecognized Record.V must be a hard error for the whole
// file, never a silent guess at an evolved shape.
func TestParseTranscriptFile_UnknownSchemaVersion_FailsLoud(t *testing.T) {
	testsupport.Isolate(t)
	line := `{"v": 99, "harp": "future-harp", "engine": "codex", "seq": 0, "ts": "2026-07-15T00:00:00Z", "kind": "entry", "entry": {"type": "user", "content": "hi"}}` + "\n"
	dst := writeTempTranscript(t, "future-harp", []byte(line))

	_, err := ParseTranscriptFile(dst, "future-harp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version")
}

// TestCanonicalHistory_ListSessions_And_CurrentSession exercises the
// project-scoped enumeration path end to end: two harps captured under the
// same project, minted in oldest-first order so their (real, monotonically
// increasing) StartedAt values order them deterministically — neither harp
// gets a legacy TranscriptPath, so sessions.ActivityTime falls back to
// StartedAt for ordering (see sessions.ActivityTime). ListSessions must
// return both, most-recent-first, with accurate EntryCount; CurrentSession
// must return the newer one's full session. A harp in a different project,
// and a harp with NO canonical transcript captured at all, must both be
// excluded.
func TestCanonicalHistory_ListSessions_And_CurrentSession(t *testing.T) {
	testsupport.Isolate(t)
	ctx := context.Background()
	const projectDir = "/proj/multi"

	older := "codex-fixture-harp"
	newer := "kiro-fixture-harp"
	installFixture(t, "codex", older)
	installFixture(t, "kiro", newer)

	store := sessions.NewMemStore()
	mint(t, store, older, projectDir)
	time.Sleep(2 * time.Millisecond) // force a distinct, later StartedAt
	mint(t, store, newer, projectDir)
	// A harp in a DIFFERENT project must never leak into this project's list.
	mint(t, store, "other-project-harp", "/proj/elsewhere")
	// A harp with NO canonical transcript captured yet must be silently
	// excluded (not a zero-entry phantom row) — the plan's "enumerate by
	// presence" rule.
	mint(t, store, "no-transcript-harp", projectDir)

	h := NewCanonicalHistory(projectDir, store)

	metas, err := h.ListSessions(ctx)
	require.NoError(t, err)
	require.Len(t, metas, 2, "exactly the two project harps WITH a captured canonical transcript")
	assert.Equal(t, newer, metas[0].ID, "most-recent-first by StartedAt")
	assert.Equal(t, 4, metas[0].EntryCount, "kiro fixture's 4 entry-kind lines")
	assert.Equal(t, older, metas[1].ID)
	assert.Equal(t, 5, metas[1].EntryCount, "codex fixture's 5 entry-kind lines")

	cur, err := h.CurrentSession(ctx)
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, newer, cur.ID)
	require.Len(t, cur.Entries, 4)
}

// TestCanonicalHistory_CurrentSession_NoSessions_ReturnsNilNil pins the
// "clean no sessions" contract pb.SessionReader.CurrentSession documents:
// nil, nil — never an error — for a project with nothing captured yet.
func TestCanonicalHistory_CurrentSession_NoSessions_ReturnsNilNil(t *testing.T) {
	testsupport.Isolate(t)
	h := NewCanonicalHistory("/proj/empty", sessions.NewMemStore())

	sess, err := h.CurrentSession(context.Background())
	assert.NoError(t, err)
	assert.Nil(t, sess)
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

// TestParseTranscriptFile_NothingDecoded_FailsLoud pins U144-F03: three states
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
//     from state 1 AND suppressed lm/grpc/canonical_source.go's legacy
//     fallback, which only runs when GetSession errors — so the caller got a
//     confidently empty history instead of the transcript that did exist.
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
		lines := `{"v":1,"harp":"envelope-only-harp","engine":"codex","seq":0,"ts":"2026-07-25T00:00:00Z","kind":"session","session":{"model":"m"}}` + "\n" +
			`{"v":1,"harp":"envelope-only-harp","engine":"codex","seq":1,"ts":"2026-07-25T00:00:01Z","kind":"complete","complete":{"num_turns":1}}` + "\n"
		dst := writeTempTranscript(t, "envelope-only-harp", []byte(lines))

		sess, err := ParseTranscriptFile(dst, "envelope-only-harp")
		require.NoError(t, err, "records decoded fine; they simply carry no entries — that is a real, empty conversation")
		require.NotNil(t, sess)
		assert.Empty(t, sess.Entries)
	})

	t.Run("one good line among corrupt ones still degrades to partial", func(t *testing.T) {
		lines := "{garbage\n" +
			`{"v":1,"harp":"partial-harp","engine":"codex","seq":1,"ts":"2026-07-25T00:00:01Z","kind":"entry","entry":{"type":"user","content":"hi"}}` + "\n" +
			"{more garbage\n"
		dst := writeTempTranscript(t, "partial-harp", []byte(lines))

		sess, err := ParseTranscriptFile(dst, "partial-harp")
		require.NoError(t, err)
		require.Len(t, sess.Entries, 1)
	})
}
