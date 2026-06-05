package gemini

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
)

// =============================================================================
// Gemini Session History Tests
// =============================================================================

func TestGeminiSessionHistory_New(t *testing.T) {
	backend := NewGemini(writeGeminiSettings)
	history := NewGeminiSessionHistory(backend)

	assert.NotNil(t, history)
	assert.Equal(t, backend, history.backend)
	assert.NotNil(t, history.fs)
}

func TestGeminiSessionHistory_WithOptions(t *testing.T) {
	backend := NewGemini(writeGeminiSettings)
	fs := afero.NewMemMapFs()

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir("/test/home"),
	)

	assert.NotNil(t, history)
	assert.Equal(t, fs, history.fs)
	assert.Equal(t, "/test/home", history.homeDir)
}

// geminiLegacyProjectDir computes the legacy (pre-slug) Gemini project directory
// for a workDir: ~/.gemini/tmp/<sha256(abspath)>. findProjectDir still resolves
// this via its sha256 fallback when no .project_root match exists, so it remains
// a valid fixture layout.
func geminiLegacyProjectDir(homeDir, workDir string) string {
	hash := sha256.Sum256([]byte(workDir))
	projectHash := hex.EncodeToString(hash[:])
	return filepath.Join(homeDir, ".gemini", "tmp", projectHash)
}

// writeGeminiSlugProject lays out a current-Gemini project store:
// ~/.gemini/tmp/<slug>/ with a .project_root recording absWorkDir and a chats/
// directory. Returns the chats directory.
func writeGeminiSlugProject(t *testing.T, fs afero.Fs, homeDir, slug, absWorkDir string) string {
	t.Helper()
	projectDir := filepath.Join(homeDir, ".gemini", "tmp", slug)
	chatsDir := filepath.Join(projectDir, "chats")
	require.NoError(t, fs.MkdirAll(chatsDir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, ".project_root"), []byte(absWorkDir), 0644))
	return chatsDir
}

// realGeminiJSONL mirrors a current Gemini 0.44 transcript: a header line
// carrying sessionId, interleaved null/typeless records, a user turn whose
// content is a [{"text":...}] array, a gemini turn whose content is a plain
// string, and an info line that carries no conversational content.
const realGeminiJSONL = `{"sessionId":"abc-123","projectHash":"deadbeef","startTime":"2026-06-02T05:03:00Z","lastUpdated":"2026-06-02T05:03:05Z","kind":"main"}
{"id":"l1","timestamp":"2026-06-02T05:03:00Z","content":null}
{"id":"l2","timestamp":"2026-06-02T05:03:01Z","type":"user","content":[{"text":"Reply with exactly: OK"}]}
{"id":"l3","timestamp":"2026-06-02T05:03:02Z","content":null}
{"id":"l4","timestamp":"2026-06-02T05:03:03Z","type":"gemini","content":"OK"}
{"id":"l5","timestamp":"2026-06-02T05:03:04Z","type":"info","content":"Update available"}
`

func TestGeminiSessionHistory_ParseRealJSONL(t *testing.T) {
	fs := afero.NewMemMapFs()
	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings), WithGeminiSessionFS(fs))

	path := "/x/session-2026-06-02T05-03-abc.jsonl"
	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(realGeminiJSONL), 0644))

	session, err := history.GetSessionByPath(path)
	require.NoError(t, err)

	// ID comes from the header line, not the filename.
	assert.Equal(t, "abc-123", session.ID)
	// Only the two conversational turns survive; header, nulls, and info drop.
	require.Len(t, session.Entries, 2)
	assert.Equal(t, EntryTypeUser, session.Entries[0].Type)
	assert.Equal(t, "Reply with exactly: OK", session.Entries[0].Content)
	assert.Equal(t, EntryTypeAssistant, session.Entries[1].Type)
	assert.Equal(t, "OK", session.Entries[1].Content)
}

func TestGeminiSessionHistory_ResolveViaProjectRoot(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/work/proj"
	// Current layout: slug dir name, real path in .project_root.
	chatsDir := writeGeminiSlugProject(t, fs, homeDir, "proj", workDir)
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "session-1.jsonl"), []byte(realGeminiJSONL), 0644))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	dir, err := history.findProjectDir(workDir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(homeDir, ".gemini", "tmp", "proj"), dir)

	session, err := history.GetCurrentSession(workDir)
	require.NoError(t, err)
	assert.Equal(t, "abc-123", session.ID)
	assert.Len(t, session.Entries, 2)
}

func TestGeminiSessionHistory_ListSessions(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/test/project"
	chatsDir := writeGeminiSlugProject(t, fs, homeDir, "project", workDir)

	// Mix of current (.jsonl) and legacy (.json) transcripts.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "session1.jsonl"), []byte(realGeminiJSONL), 0644))
	legacy := `{"messages":[{"role":"user","content":"Second","timestamp":"2024-01-15T11:00:00Z"}]}`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "session2.json"), []byte(legacy), 0644))
	// Non-session file that should be ignored.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "config.txt"), []byte("{}"), 0644))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	sessions, err := history.ListSessions(workDir)
	require.NoError(t, err)

	assert.Len(t, sessions, 2)
	assert.Contains(t, []string{"session1", "session2"}, sessions[0].ID)
	assert.Contains(t, []string{"session1", "session2"}, sessions[1].ID)
}

func TestGeminiSessionHistory_ListSessions_Empty(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/test/project"
	writeGeminiSlugProject(t, fs, homeDir, "project", workDir)

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	sessions, err := history.ListSessions(workDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestGeminiSessionHistory_ListSessions_ProjectNotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir("/test/home"))

	_, err := history.ListSessions("/test/project")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "project directory not found")
}

// TestGeminiSessionHistory_GetSession_Legacy exercises the legacy whole-file
// {"messages":[{"role"...}]} form, which the parser still accepts.
func TestGeminiSessionHistory_GetSession_Legacy(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/test/project"
	// Legacy stores lived under the sha256 dir; findProjectDir's fallback finds it.
	chatsDir := filepath.Join(geminiLegacyProjectDir(homeDir, workDir), "chats")
	require.NoError(t, fs.MkdirAll(chatsDir, 0755))

	sessionContent := `{
		"messages": [
			{"role": "user", "content": "Hello", "timestamp": "2024-01-15T10:00:00Z"},
			{"role": "model", "content": "Hi there!", "timestamp": "2024-01-15T10:00:01Z"},
			{"role": "user", "content": "How are you?", "timestamp": "2024-01-15T10:00:02Z"},
			{"role": "assistant", "content": "I'm doing well!", "timestamp": "2024-01-15T10:00:03Z"}
		]
	}`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "test-session.json"), []byte(sessionContent), 0644))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	session, err := history.GetSession(workDir, "test-session")
	require.NoError(t, err)

	assert.Equal(t, "test-session", session.ID)
	require.Len(t, session.Entries, 4)
	assert.Equal(t, EntryTypeUser, session.Entries[0].Type)
	assert.Equal(t, "Hello", session.Entries[0].Content)
	assert.Equal(t, EntryTypeAssistant, session.Entries[1].Type)
	assert.Equal(t, "Hi there!", session.Entries[1].Content)
	assert.Equal(t, EntryTypeUser, session.Entries[2].Type)
	assert.Equal(t, EntryTypeAssistant, session.Entries[3].Type)
}

// TestGeminiSessionHistory_GetSession_ByHeaderUUID verifies a lookup by the
// session UUID (what the index binds and the compactor uses) resolves even
// though the filename stem differs from the UUID — the UUID lives in the header.
func TestGeminiSessionHistory_GetSession_ByHeaderUUID(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/work/proj"
	chatsDir := writeGeminiSlugProject(t, fs, homeDir, "proj", workDir)
	// Filename stem (...deadbe) differs from the header sessionId ("abc-123").
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "session-2026-06-02T05-03-deadbe.jsonl"), []byte(realGeminiJSONL), 0644))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	s, err := history.GetSession(workDir, "abc-123")
	require.NoError(t, err)
	assert.Equal(t, "abc-123", s.ID)
	assert.Len(t, s.Entries, 2)
}

func TestGeminiSessionHistory_GetSession_NotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/test/project"
	writeGeminiSlugProject(t, fs, homeDir, "project", workDir)

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	_, err := history.GetSession(workDir, "nonexistent")
	assert.Error(t, err)
}

func TestGeminiSessionHistory_GetSessionByPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	sessionPath := "/some/path/session.jsonl"
	require.NoError(t, fs.MkdirAll(filepath.Dir(sessionPath), 0755))
	require.NoError(t, afero.WriteFile(fs, sessionPath, []byte(realGeminiJSONL), 0644))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir("/test/home"))

	session, err := history.GetSessionByPath(sessionPath)
	require.NoError(t, err)

	assert.Equal(t, "abc-123", session.ID) // from header, not filename
	assert.Len(t, session.Entries, 2)
}

func TestGeminiSessionHistory_GetCurrentSession_NoSessions(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/test/project"
	writeGeminiSlugProject(t, fs, homeDir, "project", workDir)

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	_, err := history.GetCurrentSession(workDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no sessions found")
}

func TestGeminiSessionHistory_ConvertEntry(t *testing.T) {
	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings))

	tests := []struct {
		name     string
		entry    geminiEntry
		expected SessionEntryType
		content  string
		isNil    bool
	}{
		{
			name:     "user with array content",
			entry:    geminiEntry{Type: "user", Content: json.RawMessage(`[{"text":"Hello"}]`), Timestamp: "2024-01-15T10:00:00Z"},
			expected: EntryTypeUser,
			content:  "Hello",
		},
		{
			name:     "gemini with string content",
			entry:    geminiEntry{Type: "gemini", Content: json.RawMessage(`"Response"`), Timestamp: "2024-01-15T10:00:00Z"},
			expected: EntryTypeAssistant,
			content:  "Response",
		},
		{
			name:     "legacy role fallback",
			entry:    geminiEntry{Role: "assistant", Content: json.RawMessage(`"Also works"`)},
			expected: EntryTypeAssistant,
			content:  "Also works",
		},
		{
			name:     "error becomes system",
			entry:    geminiEntry{Type: "error", Content: json.RawMessage(`"boom"`)},
			expected: EntryTypeSystem,
			content:  "boom",
		},
		{
			name:  "info dropped",
			entry: geminiEntry{Type: "info", Content: json.RawMessage(`"Update available"`)},
			isNil: true,
		},
		{
			name:  "null content dropped",
			entry: geminiEntry{Type: "gemini", Content: json.RawMessage(`null`)},
			isNil: true,
		},
		{
			name:  "typeless record dropped",
			entry: geminiEntry{Content: json.RawMessage(`null`)},
			isNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := history.convertEntry(tt.entry)
			if tt.isNil {
				assert.Nil(t, entry)
			} else {
				require.NotNil(t, entry)
				assert.Equal(t, tt.expected, entry.Type)
				assert.Equal(t, tt.content, entry.Content)
			}
		})
	}
}

// TestGeminiSessionHistory_ParseSession_Garbage asserts graceful degradation:
// an unparseable transcript yields an empty session, never an error (the
// fault-tolerance contract — recovery must never block on a bad transcript).
func TestGeminiSessionHistory_ParseSession_Garbage(t *testing.T) {
	fs := afero.NewMemMapFs()
	sessionPath := "/test/session.jsonl"
	require.NoError(t, fs.MkdirAll(filepath.Dir(sessionPath), 0755))
	require.NoError(t, afero.WriteFile(fs, sessionPath, []byte("not json\nstill not json"), 0644))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir("/test/home"))

	session, err := history.GetSessionByPath(sessionPath)
	require.NoError(t, err)
	assert.Empty(t, session.Entries)
}

func TestGeminiSessionHistory_FindProjectDir_LegacyFallback(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/test/home"
	workDir := "/test/project"
	// No .project_root anywhere; only the legacy sha256 dir exists.
	projectDir := geminiLegacyProjectDir(homeDir, workDir)
	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings),
		WithGeminiSessionFS(fs), WithGeminiSessionHomeDir(homeDir))

	result, err := history.findProjectDir(workDir)
	require.NoError(t, err)
	assert.Equal(t, projectDir, result)
}

func TestPreviousSessionByIndex(t *testing.T) {
	// Index entries are most-recent-first; the active harp ("self") is index 0.
	entries := []sessions.Entry{
		{HarpName: "self", TranscriptPath: "/t/self.jsonl"},
		{HarpName: "prev", TranscriptPath: "/t/prev.jsonl"},
		{HarpName: "old", TranscriptPath: "/t/old.jsonl"},
	}
	list := func(string) ([]sessions.Entry, error) { return entries, nil }
	byPath := func(p string) (*Session, error) { return &Session{ID: p}, nil }

	t.Run("skips self and returns most-recent prior", func(t *testing.T) {
		s, err := previousSessionByIndex("/proj", "self", list, byPath)
		require.NoError(t, err)
		require.NotNil(t, s)
		assert.Equal(t, "/t/prev.jsonl", s.ID)
	})

	t.Run("skips entries without a transcript path", func(t *testing.T) {
		list := func(string) ([]sessions.Entry, error) {
			return []sessions.Entry{
				{HarpName: "self", TranscriptPath: "/t/self.jsonl"},
				{HarpName: "pending", TranscriptPath: ""}, // never bound
				{HarpName: "prev", TranscriptPath: "/t/prev.jsonl"},
			}, nil
		}
		s, err := previousSessionByIndex("/proj", "self", list, byPath)
		require.NoError(t, err)
		require.NotNil(t, s)
		assert.Equal(t, "/t/prev.jsonl", s.ID)
	})

	t.Run("nil when only the active harp exists", func(t *testing.T) {
		list := func(string) ([]sessions.Entry, error) {
			return []sessions.Entry{{HarpName: "self", TranscriptPath: "/t/self.jsonl"}}, nil
		}
		s, err := previousSessionByIndex("/proj", "self", list, byPath)
		require.NoError(t, err)
		assert.Nil(t, s)
	})
}

func TestGeminiSessionHistory_TranscriptPathFromHook(t *testing.T) {
	history := NewGeminiSessionHistory(NewGemini(writeGeminiSettings))

	// Gemini returns the path directly.
	path := history.TranscriptPathFromHook("/work", "session-id", "/path/to/transcript")
	assert.Equal(t, "/path/to/transcript", path)
}
