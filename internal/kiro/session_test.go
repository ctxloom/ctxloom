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
// /kiro-home (via the KIRO_HOME seam) with the given files.
func newFixtureHistory(t *testing.T, files map[string]string) *kiroSessionHistory {
	t.Helper()
	fs := afero.NewMemMapFs()
	for name, content := range files {
		require.NoError(t, afero.WriteFile(fs, filepath.Join("/kiro-home", name), []byte(content), 0o644))
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

// TestKiroListSessions_FiltersByCwd: only sessions whose metadata cwd matches
// the workDir are listed; sessions without a readable sidecar are skipped for
// a scoped listing (their directory affinity is unknowable).
func TestKiroListSessions_FiltersByCwd(t *testing.T) {
	h := newFixtureHistory(t, map[string]string{
		"s1.json":  `{"cwd":"/proj","created_at":"2026-07-01T10:00:00Z"}`,
		"s1.jsonl": `{"role":"user","content":"hello"}`,
		"s2.json":  `{"cwd":"/other","created_at":"2026-07-01T11:00:00Z"}`,
		"s2.jsonl": `{"role":"user","content":"elsewhere"}`,
		"s3.jsonl": `{"role":"user","content":"no sidecar"}`,
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
// transcript comes back parsed, most-recent-first ordering honored.
func TestKiroGetCurrentSession_MostRecent(t *testing.T) {
	h := newFixtureHistory(t, map[string]string{
		"old.json":  `{"cwd":"/proj","created_at":"2026-07-01T09:00:00Z"}`,
		"old.jsonl": `{"role":"user","content":"first"}`,
		"new.json":  `{"cwd":"/proj","created_at":"2026-07-01T12:00:00Z"}`,
		"new.jsonl": "{\"role\":\"user\",\"content\":\"question\"}\n{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]}",
	})

	sess, err := h.GetCurrentSession("/proj")
	require.NoError(t, err)
	assert.Equal(t, "new", sess.ID)
	require.Len(t, sess.Entries, 2)
	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Equal(t, "question", sess.Entries[0].Content)
	assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[1].Type)
	assert.Equal(t, "answer", sess.Entries[1].Content, "block-array content flattens to text")
}

// TestParseKiroLine_Defensive pins the tolerant line parser: tool traffic maps
// to tool entries, unknown speakers and malformed lines yield nothing (partial
// transcript, never an error).
func TestParseKiroLine_Defensive(t *testing.T) {
	tool := parseKiroLine([]byte(`{"type":"tool_use","name":"fs_read","input":{"path":"x"}}`))
	require.Len(t, tool, 1)
	assert.Equal(t, agent.EntryTypeToolUse, tool[0].Type)
	assert.Equal(t, "fs_read", tool[0].ToolName)

	result := parseKiroLine([]byte(`{"type":"tool_result","name":"fs_read","output":"data","is_error":true}`))
	require.Len(t, result, 1)
	assert.Equal(t, agent.EntryTypeToolResult, result[0].Type)
	assert.True(t, result[0].IsError)

	assert.Nil(t, parseKiroLine([]byte(`{"type":"metadata","version":3}`)), "unknown speaker → skipped")
	assert.Nil(t, parseKiroLine([]byte(`not json`)), "malformed → skipped")
}

// TestKiroSessions_DefaultHome: without KIRO_HOME the store resolves under
// ~/.kiro (via the HomeDir override).
func TestKiroSessions_DefaultHome(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/home/u/.kiro/s.json", []byte(`{"cwd":"/proj"}`), 0o644))
	require.NoError(t, afero.WriteFile(fs, "/home/u/.kiro/s.jsonl", []byte(`{"role":"user","content":"hi"}`), 0o644))
	h := newKiroSessionHistory()
	h.FS = fs
	h.HomeDir = "/home/u"
	h.getenv = func(string) string { return "" }

	sessions, err := h.ListSessions("/proj")
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "s", sessions[0].ID)
}
