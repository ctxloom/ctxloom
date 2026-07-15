package antigravity

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// transcript lines below mirror a verbatim agy v1.0.7 transcript_full.jsonl
// (2026-06-10): step records keyed by source/type with created_at timestamps.
const sampleTranscript = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-10T20:05:07Z","content":"<USER_REQUEST>\nRun the shell command: echo combo-probe-2 and show its output\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: 2026-06-10T15:05:07-05:00.\n</ADDITIONAL_METADATA>"}
{"step_index":1,"source":"SYSTEM","type":"CONVERSATION_HISTORY","status":"DONE","created_at":"2026-06-10T20:05:07Z"}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-10T20:05:07Z","tool_calls":[{"name":"run_command","args":{"CommandLine":"echo combo-probe-2","Cwd":"/tmp/agy-probe","WaitMsBeforeAsync":2000}}]}
{"step_index":3,"source":"MODEL","type":"RUN_COMMAND","status":"DONE","created_at":"2026-06-10T20:05:08Z","content":"combo-probe-2"}
{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-10T20:05:10Z","content":"The command printed combo-probe-2."}
`

const sampleConversationID = "a404a6e2-2bc3-466d-86f7-4abca16ffb04"

func writeSampleBrain(t *testing.T, fs afero.Fs, home string) string {
	t.Helper()
	path := filepath.Join(home, ".gemini", "antigravity-cli", "brain", sampleConversationID,
		".system_generated", "logs", "transcript_full.jsonl")
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(sampleTranscript), 0644))
	return path
}

func newTestHistory(fs afero.Fs) *AntigravitySessionHistory {
	return NewAntigravitySessionHistory(nil,
		WithAntigravitySessionFS(fs),
		WithAntigravitySessionHomeDir("/home/u"),
	)
}

func TestAntigravitySessionHistory_ListAndGet(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeSampleBrain(t, fs, "/home/u")
	h := newTestHistory(fs)

	metas, err := h.ListSessions("/anywhere")
	require.NoError(t, err)
	require.Len(t, metas, 1)
	assert.Equal(t, sampleConversationID, metas[0].ID)

	session, err := h.GetSession("/anywhere", sampleConversationID)
	require.NoError(t, err)
	assert.Equal(t, sampleConversationID, session.ID)

	require.Len(t, session.Entries, 4)

	// USER_INPUT: <USER_REQUEST> wrapper stripped, metadata block dropped.
	assert.Equal(t, agent.EntryTypeUser, session.Entries[0].Type)
	assert.Equal(t, "Run the shell command: echo combo-probe-2 and show its output", session.Entries[0].Content)

	// PLANNER_RESPONSE tool_calls become tool-use entries.
	assert.Equal(t, agent.EntryTypeToolUse, session.Entries[1].Type)
	assert.Equal(t, "run_command", session.Entries[1].ToolName)

	// Tool execution record becomes a tool result.
	assert.Equal(t, agent.EntryTypeToolResult, session.Entries[2].Type)
	assert.Equal(t, "combo-probe-2", session.Entries[2].ToolOutput)

	// Text PLANNER_RESPONSE becomes an assistant entry.
	assert.Equal(t, agent.EntryTypeAssistant, session.Entries[3].Type)
	assert.Equal(t, "The command printed combo-probe-2.", session.Entries[3].Content)

	// Times stamped from first/last entries.
	assert.False(t, session.StartTime.IsZero())
	assert.False(t, session.EndTime.Before(session.StartTime))
}

func TestAntigravitySessionHistory_GetCurrentSession(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeSampleBrain(t, fs, "/home/u")
	h := newTestHistory(fs)

	session, err := h.GetCurrentSession("/anywhere")
	require.NoError(t, err)
	assert.Equal(t, sampleConversationID, session.ID)
}

// writeBrainConversation writes a minimal one-entry transcript for
// conversationID and returns its path, for the G-session workspace-map tests
// below (writeSampleBrain always uses the fixed sampleConversationID, so a
// two-workspace test needs a second helper that takes an id).
func writeBrainConversation(t *testing.T, fs afero.Fs, home, conversationID, content string) string {
	t.Helper()
	path := filepath.Join(home, ".gemini", "antigravity-cli", "brain", conversationID,
		".system_generated", "logs", "transcript_full.jsonl")
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(content), 0644))
	return path
}

// writeLastConversations writes agy's workDir -> conversation-UUID map
// (cache/last_conversations.json, VERIFIED shape) for the G-session tests.
func writeLastConversations(t *testing.T, fs afero.Fs, home string, m map[string]string) {
	t.Helper()
	path := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "last_conversations.json")
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	data, err := json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, afero.WriteFile(fs, path, data, 0644))
}

// TestGetCurrentSession_PrefersWorkspaceMapOverGlobalMtime is the G-session
// payload test (plan §8 item 4): two workspaces are mapped to two distinct
// conversations, and wsB's transcript is deliberately made NEWER by mtime
// than wsA's. Before the fix, GetCurrentSession picked the global
// mtime-newest transcript regardless of workDir — so wsA's request would
// silently return wsB's (unrelated) conversation. The fix must return wsA's
// mapped conversation even though it is NOT the most recently modified.
func TestGetCurrentSession_PrefersWorkspaceMapOverGlobalMtime(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/home/u"
	const wsA, wsB = "/proj/a", "/proj/b"
	const idA, idB = "aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002"

	pathA := writeBrainConversation(t, fs, home, idA,
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-10T20:05:07Z","content":"<USER_REQUEST>from A</USER_REQUEST>"}`+"\n")
	pathB := writeBrainConversation(t, fs, home, idB,
		`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-10T20:05:07Z","content":"<USER_REQUEST>from B</USER_REQUEST>"}`+"\n")
	writeLastConversations(t, fs, home, map[string]string{wsA: idA, wsB: idB})

	// wsB's transcript is newer by mtime than wsA's — the exact condition that
	// used to make the global mtime-newest picker return the WRONG workspace.
	older := time.Date(2026, 6, 10, 20, 5, 7, 0, time.UTC)
	newer := time.Date(2026, 6, 10, 21, 0, 0, 0, time.UTC)
	require.NoError(t, fs.Chtimes(pathA, older, older))
	require.NoError(t, fs.Chtimes(pathB, newer, newer))

	h := NewAntigravitySessionHistory(nil, WithAntigravitySessionFS(fs), WithAntigravitySessionHomeDir(home))

	session, err := h.GetCurrentSession(wsA)
	require.NoError(t, err)
	assert.Equal(t, idA, session.ID, "wsA's mapped conversation must win over wsB's newer-mtime one")
	require.Len(t, session.Entries, 1)
	assert.Equal(t, "from A", session.Entries[0].Content)

	session, err = h.GetCurrentSession(wsB)
	require.NoError(t, err)
	assert.Equal(t, idB, session.ID)
	assert.Equal(t, "from B", session.Entries[0].Content)
}

// TestGetCurrentSession_UnmappedWorkspaceFallsBackToMtimeNewest proves an
// unmapped workDir (absent from last_conversations.json) still gets a
// best-effort answer via the pre-fix global mtime-newest behavior, rather
// than erroring — matching codex's global-store trade-off.
func TestGetCurrentSession_UnmappedWorkspaceFallsBackToMtimeNewest(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/home/u"
	writeSampleBrain(t, fs, home)
	writeLastConversations(t, fs, home, map[string]string{"/some/other/workspace": "not-this-one"})

	h := NewAntigravitySessionHistory(nil, WithAntigravitySessionFS(fs), WithAntigravitySessionHomeDir(home))
	session, err := h.GetCurrentSession("/proj/unmapped")
	require.NoError(t, err)
	assert.Equal(t, sampleConversationID, session.ID, "unmapped workDir falls back to mtime-newest")
}

// TestGetCurrentSession_NoLastConversationsFile proves a fresh install (no
// cache/last_conversations.json at all) degrades to the mtime-newest
// fallback rather than erroring.
func TestGetCurrentSession_NoLastConversationsFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	home := "/home/u"
	writeSampleBrain(t, fs, home)

	h := NewAntigravitySessionHistory(nil, WithAntigravitySessionFS(fs), WithAntigravitySessionHomeDir(home))
	session, err := h.GetCurrentSession("/proj/whatever")
	require.NoError(t, err)
	assert.Equal(t, sampleConversationID, session.ID)
}

func TestAntigravitySessionHistory_GetSessionByPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := writeSampleBrain(t, fs, "/home/u")
	h := newTestHistory(fs)

	session, err := h.GetSessionByPath(path)
	require.NoError(t, err)
	assert.Equal(t, sampleConversationID, session.ID, "conversation ID recovered from the brain path")
	assert.NotEmpty(t, session.Entries)
}

func TestAntigravitySessionHistory_NoBrainDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := newTestHistory(fs)

	metas, err := h.ListSessions("/anywhere")
	require.NoError(t, err)
	assert.Empty(t, metas)

	_, err = h.GetSession("/anywhere", "nope")
	assert.Error(t, err)
}

func TestAntigravitySessionHistory_TranscriptPathFromHook(t *testing.T) {
	h := newTestHistory(afero.NewMemMapFs())
	assert.Equal(t, "/some/path.jsonl", h.TranscriptPathFromHook("/ws", "id", "/some/path.jsonl"))
}

// TestAntigravitySessionHistory_OversizedLineDegrades pins the
// degrade-to-partial contract: agy embeds whole file contents in single JSONL
// lines (write_to_file CodeContent), so a line far beyond any scanner cap
// must parse — or at worst be skipped — without failing the session.
func TestAntigravitySessionHistory_OversizedLineDegrades(t *testing.T) {
	fs := afero.NewMemMapFs()
	huge := strings.Repeat("x", 5*1024*1024)
	transcript := `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-06-10T20:05:07Z","content":"<USER_REQUEST>before</USER_REQUEST>"}` + "\n" +
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-10T20:05:08Z","content":"` + huge + `"}` + "\n" +
		`{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-06-10T20:05:09Z","content":"after"}` + "\n"
	path := filepath.Join("/home/u", ".gemini", "antigravity-cli", "brain", "big",
		".system_generated", "logs", "transcript_full.jsonl")
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(transcript), 0644))

	h := newTestHistory(fs)
	session, err := h.GetSession("/anywhere", "big")
	require.NoError(t, err, "an oversized line must never fail the whole session")
	require.Len(t, session.Entries, 3, "the oversized entry itself parses")
	assert.Equal(t, "before", session.Entries[0].Content)
	assert.Equal(t, "after", session.Entries[2].Content)
}

func TestExtractUserRequest(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"wrapped", "<USER_REQUEST>\nhello\n</USER_REQUEST>\n<ADDITIONAL_METADATA>x</ADDITIONAL_METADATA>", "hello"},
		{"unwrapped", "  plain text  ", "plain text"},
		{"unterminated", "<USER_REQUEST>\ntail", "tail"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, extractUserRequest(tt.in))
		})
	}
}
