// Hermetic tests for CodexSessionHistory. Mirrors the gemini_session_test.go
// pattern: synthetic homeDir + afero.MemMapFs so we exercise the codex
// rollout JSONL parser, the YYYY/MM/DD walk, and the entry-type switch
// without touching ~/.codex.
package backends

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexSessionsDir is the per-test sessions root: <homeDir>/.codex/sessions.
// Matches getSessionsDir's resolution when homeDir override is set.
func codexSessionsDir(homeDir string) string {
	return filepath.Join(homeDir, ".codex", "sessions")
}

// =============================================================================
// Construction
// =============================================================================

func TestNewCodexSessionHistory_Defaults(t *testing.T) {
	b := NewCodex()
	h := NewCodexSessionHistory(b)

	assert.Same(t, b, h.backend, "backend must be wired through")
	assert.NotNil(t, h.fs, "default fs is OS")
	assert.Empty(t, h.homeDir, "default homeDir is empty (fall back to UserHomeDir)")
}

func TestNewCodexSessionHistory_WithOptions(t *testing.T) {
	fs := afero.NewMemMapFs()
	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/synthetic/home"),
	)
	assert.Same(t, fs, h.fs)
	assert.Equal(t, "/synthetic/home", h.homeDir)
}

// =============================================================================
// getSessionsDir
// =============================================================================

func TestCodexSessionHistory_SessionsDir_MissingErrors(t *testing.T) {
	fs := afero.NewMemMapFs() // empty fs — no sessions dir
	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	_, err := h.ListSessions("/proj")
	require.Error(t, err, "missing sessions dir must surface as error")
	assert.Contains(t, err.Error(), "sessions directory not found")
}

func TestCodexSessionHistory_SessionsDir_FoundWhenPresent(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(codexSessionsDir("/h"), 0755))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	got, err := h.ListSessions("/proj")
	require.NoError(t, err)
	assert.Empty(t, got, "empty sessions dir yields no entries (no error)")
}

// =============================================================================
// ListSessions: walk + filter
// =============================================================================

func TestCodexSessionHistory_ListSessions_FiltersAndWalks(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := codexSessionsDir("/h")
	day := filepath.Join(root, "2026", "05", "27")
	require.NoError(t, fs.MkdirAll(day, 0755))

	// Two valid rollout files in YYYY/MM/DD plus one non-rollout file and
	// one wrong-extension file that must be skipped.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(day, "rollout-a.jsonl"), []byte(""), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(day, "rollout-b.jsonl"), []byte(""), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(day, "notes.txt"), []byte(""), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(day, "rollout-c.txt"), []byte(""), 0644))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	got, err := h.ListSessions("/proj")
	require.NoError(t, err)
	require.Len(t, got, 2, "only rollout-*.jsonl entries are listed")

	// IDs are paths relative to the sessions dir — so they include the
	// YYYY/MM/DD prefix. Order is most-recent-first, but mod-times are
	// equal here, so just check the set.
	ids := []string{got[0].ID, got[1].ID}
	assert.Contains(t, ids, filepath.Join("2026", "05", "27", "rollout-a.jsonl"))
	assert.Contains(t, ids, filepath.Join("2026", "05", "27", "rollout-b.jsonl"))
}

// =============================================================================
// parseSessionFile: JSONL parsing + entry-type dispatch
// =============================================================================

func TestCodexSessionHistory_GetSession_ParsesUserAndAssistant(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := codexSessionsDir("/h")
	day := filepath.Join(root, "2026", "05", "27")
	require.NoError(t, fs.MkdirAll(day, 0755))

	content := `{"type":"message","role":"user","content":"hi","timestamp":"2026-05-27T10:00:00Z"}
{"type":"message","role":"assistant","content":"hello back","timestamp":"2026-05-27T10:00:05Z"}
`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(day, "rollout-x.jsonl"), []byte(content), 0644))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	sessions, err := h.ListSessions("/proj")
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	got, err := h.GetSession("/proj", sessions[0].ID)
	require.NoError(t, err)
	require.Len(t, got.Entries, 2)

	assert.Equal(t, EntryTypeUser, got.Entries[0].Type)
	assert.Equal(t, "hi", got.Entries[0].Content)
	assert.Equal(t, EntryTypeAssistant, got.Entries[1].Type)
	assert.Equal(t, "hello back", got.Entries[1].Content)

	// Start/end times derived from first/last entry timestamps.
	assert.False(t, got.StartTime.IsZero(), "StartTime is set when entries have timestamps")
	assert.True(t, got.EndTime.After(got.StartTime), "EndTime follows StartTime")
}

func TestCodexSessionHistory_GetSession_ParsesToolUseAndResult(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := codexSessionsDir("/h")
	require.NoError(t, fs.MkdirAll(root, 0755))

	content := `{"type":"tool_use","tool_name":"Bash","tool_input":{"command":"ls"},"timestamp":"2026-05-27T10:00:00Z"}
{"type":"tool_result","tool_name":"Bash","output":"file1\nfile2","is_error":false,"timestamp":"2026-05-27T10:00:01Z"}
{"type":"codex.tool_decision","tool_name":"Edit","tool_input":{"path":"/x"},"timestamp":"2026-05-27T10:00:02Z"}
{"type":"codex.tool_result","tool_name":"Edit","output":"oops","is_error":true,"timestamp":"2026-05-27T10:00:03Z"}
`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(root, "rollout-t.jsonl"), []byte(content), 0644))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	got, err := h.GetSession("/proj", "rollout-t.jsonl")
	require.NoError(t, err)
	require.Len(t, got.Entries, 4)

	assert.Equal(t, EntryTypeToolUse, got.Entries[0].Type)
	assert.Equal(t, "Bash", got.Entries[0].ToolName)

	assert.Equal(t, EntryTypeToolResult, got.Entries[1].Type)
	assert.Equal(t, "file1\nfile2", got.Entries[1].ToolOutput)
	assert.False(t, got.Entries[1].IsError)

	// codex.tool_decision aliases to ToolUse.
	assert.Equal(t, EntryTypeToolUse, got.Entries[2].Type)
	assert.Equal(t, "Edit", got.Entries[2].ToolName)

	// codex.tool_result with is_error=true.
	assert.Equal(t, EntryTypeToolResult, got.Entries[3].Type)
	assert.True(t, got.Entries[3].IsError)
}

func TestCodexSessionHistory_GetSession_SkipsUnknownAndMalformed(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := codexSessionsDir("/h")
	require.NoError(t, fs.MkdirAll(root, 0755))

	// Mix: blank line, malformed JSON, unknown "type", unknown role on
	// "message", then one valid entry. The valid one should survive.
	content := "" +
		"\n" + // blank line
		"not json\n" + // malformed
		`{"type":"unknown","timestamp":"2026-05-27T10:00:00Z"}` + "\n" + // unknown type
		`{"type":"message","role":"system","content":"x"}` + "\n" + // unknown role
		`{"type":"message","role":"user","content":"survived","timestamp":"2026-05-27T10:00:00Z"}` + "\n"
	require.NoError(t, afero.WriteFile(fs, filepath.Join(root, "rollout-s.jsonl"), []byte(content), 0644))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	got, err := h.GetSession("/proj", "rollout-s.jsonl")
	require.NoError(t, err)
	require.Len(t, got.Entries, 1, "only the single well-formed user entry survives")
	assert.Equal(t, "survived", got.Entries[0].Content)
}

func TestCodexSessionHistory_GetSession_MissingFileErrors(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(codexSessionsDir("/h"), 0755))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	_, err := h.GetSession("/proj", "rollout-nope.jsonl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open session file")
}

// =============================================================================
// GetCurrentSession + GetSessionByPath
// =============================================================================

func TestCodexSessionHistory_GetCurrentSession_NoneFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(codexSessionsDir("/h"), 0755))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	_, err := h.GetCurrentSession("/proj")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sessions found")
}

func TestCodexSessionHistory_GetCurrentSession_ReturnsAvailable(t *testing.T) {
	fs := afero.NewMemMapFs()
	root := codexSessionsDir("/h")
	require.NoError(t, fs.MkdirAll(root, 0755))

	content := `{"type":"message","role":"user","content":"hi","timestamp":"2026-05-27T10:00:00Z"}` + "\n"
	require.NoError(t, afero.WriteFile(fs, filepath.Join(root, "rollout-cur.jsonl"), []byte(content), 0644))

	h := NewCodexSessionHistory(NewCodex(),
		WithCodexSessionFS(fs),
		WithCodexSessionHomeDir("/h"),
	)
	got, err := h.GetCurrentSession("/proj")
	require.NoError(t, err)
	require.Len(t, got.Entries, 1)
	assert.Equal(t, "hi", got.Entries[0].Content)
}

func TestCodexSessionHistory_GetSessionByPath(t *testing.T) {
	// GetSessionByPath bypasses the sessions-dir resolution and reads
	// the path directly through the injected fs.
	fs := afero.NewMemMapFs()
	path := "/somewhere/else/rollout-by-path.jsonl"
	content := `{"type":"message","role":"assistant","content":"direct","timestamp":"2026-05-27T10:00:00Z"}` + "\n"
	require.NoError(t, afero.WriteFile(fs, path, []byte(content), 0644))

	h := NewCodexSessionHistory(NewCodex(), WithCodexSessionFS(fs))
	got, err := h.GetSessionByPath(path)
	require.NoError(t, err)
	require.Len(t, got.Entries, 1)
	assert.Equal(t, EntryTypeAssistant, got.Entries[0].Type)
}

// =============================================================================
// No-op registry methods (no codex registry support yet)
// =============================================================================

func TestCodexSessionHistory_RegistryNoOps(t *testing.T) {
	h := NewCodexSessionHistory(NewCodex())

	// TranscriptPathFromHook always returns "".
	assert.Empty(t, h.TranscriptPathFromHook("/proj", "session-id", "/tmp/x.jsonl"))
}
