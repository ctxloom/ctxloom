package kiro

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// newFixtureHistory builds a session reader over an in-memory store rooted at
// /kiro-home/sessions/cli (via the KIRO_HOME seam) with the given files — the
// real on-disk layout confirmed against a live kiro-cli 2.12.1.
func newFixtureHistory(t *testing.T, files map[string]string) *kiroSessionHistory {
	t.Helper()
	fs := afero.NewMemMapFs()
	for name, content := range files {
		require.NoError(t, afero.WriteFile(fs, filepath.Join("/kiro-home", "sessions", "cli", name), []byte(content), 0o644))
	}
	h := newKiroSessionHistory()
	h.FS = fs
	h.getenv = func(k string) string {
		if k == "KIRO_HOME" {
			return "/kiro-home"
		}
		return ""
	}
	return h
}

// TestKiroStoreDir_DescendsIntoSessionsCli pins the real store layout: the
// session tuple lives under KIRO_HOME/sessions/cli, not directly under
// KIRO_HOME (which also holds agents/settings/skills/steering as siblings of
// sessions/).
func TestKiroStoreDir_DescendsIntoSessionsCli(t *testing.T) {
	h := newKiroSessionHistory()
	h.getenv = func(k string) string {
		if k == "KIRO_HOME" {
			return "/kiro-home"
		}
		return ""
	}
	dir, err := h.storeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/kiro-home", "sessions", "cli"), dir)

	h2 := newKiroSessionHistory()
	h2.HomeDir = "/home/u"
	h2.getenv = func(string) string { return "" }
	dir2, err := h2.storeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/home/u", ".kiro", "sessions", "cli"), dir2)
}

// TestKiroListSessions_FiltersByCwd: only sessions whose metadata cwd matches
// the workDir are listed; sessions without a readable sidecar are skipped for
// a scoped listing (their directory affinity is unknowable).
func TestKiroListSessions_FiltersByCwd(t *testing.T) {
	h := newFixtureHistory(t, map[string]string{
		"s1.json":  `{"cwd":"/proj","created_at":"2026-07-01T10:00:00Z"}`,
		"s1.jsonl": `{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"hello"}]}}`,
		"s2.json":  `{"cwd":"/other","created_at":"2026-07-01T11:00:00Z"}`,
		"s2.jsonl": `{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"elsewhere"}]}}`,
		"s3.jsonl": `{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"no sidecar"}]}}`,
	})

	sessions, err := h.ListSessions("/proj")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "s1", sessions[0].ID)
	assert.Equal(t, "2026-07-01T10:00:00Z", sessions[0].StartTime.UTC().Format("2006-01-02T15:04:05Z"))

	all, err := h.ListSessions("")
	require.NoError(t, err)
	assert.Len(t, all, 3, "an empty workDir lists every session, sidecar or not")
}

// TestKiroGetCurrentSession_MostRecent: the newest matching session's
// transcript comes back parsed, most-recent-first ordering honored, using the
// real jsonl line shape (kind/data-wrapped, not the old flat guess).
func TestKiroGetCurrentSession_MostRecent(t *testing.T) {
	h := newFixtureHistory(t, map[string]string{
		"old.json":  `{"cwd":"/proj","created_at":"2026-07-01T09:00:00Z"}`,
		"old.jsonl": `{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"first"}]}}`,
		"new.json":  `{"cwd":"/proj","created_at":"2026-07-01T12:00:00Z"}`,
		"new.jsonl": `{"version":"v1","kind":"Prompt","data":{"message_id":"m1","content":[{"kind":"text","data":"question"}],"meta":{"timestamp":1784056401}}}
{"version":"v1","kind":"AssistantMessage","data":{"message_id":"m2","content":[{"kind":"text","data":"answer"}]}}`,
	})

	sess, err := h.GetCurrentSession("/proj")
	require.NoError(t, err)
	assert.Equal(t, "new", sess.ID)
	require.Len(t, sess.Entries, 2)
	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Equal(t, "question", sess.Entries[0].Content)
	assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[1].Type)
	assert.Equal(t, "answer", sess.Entries[1].Content)
}

// TestKiroSessions_DefaultHome: without KIRO_HOME the store resolves under
// ~/.kiro/sessions/cli (via the HomeDir override).
func TestKiroSessions_DefaultHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/home/u/.kiro/sessions/cli/s.json", []byte(`{"cwd":"/proj"}`), 0o644))
	require.NoError(t, afero.WriteFile(fs, "/home/u/.kiro/sessions/cli/s.jsonl", []byte(`{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"hi"}]}}`), 0o644))
	h := newKiroSessionHistory()
	h.FS = fs
	h.HomeDir = "/home/u"
	h.getenv = func(string) string { return "" }

	sessions, err := h.ListSessions("/proj")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "s", sessions[0].ID)
}

// TestParseKiroLine_Prompt: a user turn is {"kind":"Prompt","data":{"content":
// [{"kind":"text","data":"..."}],"meta":{"timestamp":...}}} — text nested
// under a content block, not a flat field.
func TestParseKiroLine_Prompt(t *testing.T) {
	entries := parseKiroLine([]byte(`{"version":"v1","kind":"Prompt","data":{"content":[{"kind":"text","data":"hello"}],"meta":{"timestamp":1784056401}}}`))
	require.Len(t, entries, 1)
	assert.Equal(t, agent.EntryTypeUser, entries[0].Type)
	assert.Equal(t, "hello", entries[0].Content)
	assert.Equal(t, int64(1784056401), entries[0].Timestamp.Unix())
}

// TestParseKiroLine_AssistantTextOnly: a plain assistant reply with no tool
// call is one text block, no meta.timestamp (real transcripts never carry one
// on assistant lines).
func TestParseKiroLine_AssistantTextOnly(t *testing.T) {
	entries := parseKiroLine([]byte(`{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":"hi there"}]}}`))
	require.Len(t, entries, 1)
	assert.Equal(t, agent.EntryTypeAssistant, entries[0].Type)
	assert.Equal(t, "hi there", entries[0].Content)
	assert.True(t, entries[0].Timestamp.IsZero())
}

// TestParseKiroLine_AssistantToolUse: a tool-calling assistant turn carries an
// EMPTY text block alongside its toolUse block — the empty text must be
// dropped (no bogus blank assistant entry), and the toolUse block becomes a
// tool_use entry carrying the real name/input, not a name kiro never sends.
func TestParseKiroLine_AssistantToolUse(t *testing.T) {
	line := `{"version":"v1","kind":"AssistantMessage","data":{"content":[{"kind":"text","data":""},{"kind":"toolUse","data":{"toolUseId":"tooluse_abc","name":"read","input":{"operations":[{"mode":"Line","path":"probe.txt"}]}}}]}}`
	entries := parseKiroLine([]byte(line))
	require.Len(t, entries, 1, "the empty text block yields no entry")
	assert.Equal(t, agent.EntryTypeToolUse, entries[0].Type)
	assert.Equal(t, "read", entries[0].ToolName)
	assert.JSONEq(t, `{"operations":[{"mode":"Line","path":"probe.txt"}]}`, string(entries[0].ToolInput))
}

// TestParseKiroLine_ToolResults: a ToolResults line's toolResult block
// flattens to a tool_result entry; status "success" is not an error.
func TestParseKiroLine_ToolResults(t *testing.T) {
	line := `{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_abc","content":[{"kind":"text","data":"probe file contents"}],"status":"success"}}]}}`
	entries := parseKiroLine([]byte(line))
	require.Len(t, entries, 1)
	assert.Equal(t, agent.EntryTypeToolResult, entries[0].Type)
	assert.Equal(t, "probe file contents", entries[0].ToolOutput)
	assert.False(t, entries[0].IsError)
}

// TestParseKiroLine_ToolResultsError: a non-"success" status marks the entry
// an error.
func TestParseKiroLine_ToolResultsError(t *testing.T) {
	line := `{"version":"v1","kind":"ToolResults","data":{"content":[{"kind":"toolResult","data":{"toolUseId":"tooluse_abc","content":[{"kind":"text","data":"boom"}],"status":"error"}}]}}`
	entries := parseKiroLine([]byte(line))
	require.Len(t, entries, 1)
	assert.True(t, entries[0].IsError)
}

// TestParseKiroLine_Defensive: unknown kinds and malformed lines degrade to
// nothing (partial transcript, never an error).
func TestParseKiroLine_Defensive(t *testing.T) {
	assert.Nil(t, parseKiroLine([]byte(`{"version":"v1","kind":"SomeFutureKind","data":{}}`)), "unknown kind → skipped")
	assert.Nil(t, parseKiroLine([]byte(`not json`)), "malformed → skipped")
	assert.Nil(t, parseKiroLine([]byte(`{"version":"v1","kind":"Prompt","data":{"content":[]}}`)), "empty content → no entry")
}

// TestKiroSession_RealCapturedTranscript proves the reader against a REAL
// session tuple (testdata/real_session/), captured verbatim from a live,
// authenticated kiro-cli 2.12.1 driving a tool-calling turn through an actual
// pty — file paths and content sanitized, jsonl/json shape and field names
// untouched. A fixture built from assumptions would not catch a shape drift
// the way replaying genuine captured bytes does.
func TestKiroSession_RealCapturedTranscript(t *testing.T) {
	h := newKiroSessionHistory() // real OS filesystem — this fixture is read as a genuine file, not injected via afero.MemMapFs
	// Read directly by path rather than via ListSessions/storeDir: the real
	// fixture lives at testdata/real_session/, one level up from where
	// storeDir's sessions/cli descent would look, and GetSessionByPath is
	// exactly the seam that lets a caller hand in a raw path.
	sess, err := h.GetSessionByPath(filepath.Join("testdata", "real_session", "session.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, "session", sess.ID)
	require.Len(t, sess.Entries, 4, "Prompt(user) + AssistantMessage(toolUse only, empty text dropped) + ToolResults + AssistantMessage(text) = 4 normalized entries")

	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Contains(t, sess.Entries[0].Content, "probe.txt")

	assert.Equal(t, agent.EntryTypeToolUse, sess.Entries[1].Type)
	assert.Equal(t, "read", sess.Entries[1].ToolName)

	assert.Equal(t, agent.EntryTypeToolResult, sess.Entries[2].Type)
	assert.Equal(t, "probe file contents", sess.Entries[2].ToolOutput)
	assert.False(t, sess.Entries[2].IsError)

	assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[3].Type)
	assert.Contains(t, sess.Entries[3].Content, "probe file contents")
}
