package backends

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Gemini Session History Tests
// =============================================================================

func TestGeminiSessionHistory_New(t *testing.T) {
	backend := NewGemini()
	history := NewGeminiSessionHistory(backend)

	assert.NotNil(t, history)
	assert.Equal(t, backend, history.backend)
	assert.NotNil(t, history.fs)
}

func TestGeminiSessionHistory_WithOptions(t *testing.T) {
	backend := NewGemini()
	fs := afero.NewMemMapFs()

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir("/test/home"),
	)

	assert.NotNil(t, history)
	assert.Equal(t, fs, history.fs)
	assert.Equal(t, "/test/home", history.homeDir)
}

// geminiProjectDir computes the Gemini project directory path for a workDir.
// Gemini uses SHA256 hash of the absolute path.
func geminiProjectDir(homeDir, workDir string) string {
	hash := sha256.Sum256([]byte(workDir))
	projectHash := hex.EncodeToString(hash[:])
	return filepath.Join(homeDir, ".gemini", "tmp", projectHash)
}

func TestGeminiSessionHistory_ListSessions(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)
	chatsDir := filepath.Join(projectDir, "chats")

	require.NoError(t, fs.MkdirAll(chatsDir, 0755))

	// Create session files
	session1 := `{"messages":[{"role":"user","content":"Hello","timestamp":"2024-01-15T10:00:00Z"}]}`
	session2 := `{"messages":[{"role":"user","content":"Second","timestamp":"2024-01-15T11:00:00Z"}]}`

	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "session1.json"), []byte(session1), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "session2.json"), []byte(session2), 0644))

	// Create non-session file that should be ignored
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "config.txt"), []byte("{}"), 0644))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	sessions, err := history.ListSessions(workDir)
	require.NoError(t, err)

	assert.Len(t, sessions, 2)
	assert.Contains(t, []string{"session1", "session2"}, sessions[0].ID)
	assert.Contains(t, []string{"session1", "session2"}, sessions[1].ID)
}

func TestGeminiSessionHistory_ListSessions_Empty(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)
	chatsDir := filepath.Join(projectDir, "chats")

	require.NoError(t, fs.MkdirAll(chatsDir, 0755))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	sessions, err := history.ListSessions(workDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestGeminiSessionHistory_ListSessions_ProjectNotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir("/test/home"),
	)

	_, err := history.ListSessions("/test/project")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "project directory not found")
}

func TestGeminiSessionHistory_GetSession(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)
	chatsDir := filepath.Join(projectDir, "chats")

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

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	session, err := history.GetSession(workDir, "test-session")
	require.NoError(t, err)

	assert.Equal(t, "test-session.json", session.ID)
	assert.Len(t, session.Entries, 4)

	// Verify entry types
	assert.Equal(t, EntryTypeUser, session.Entries[0].Type)
	assert.Equal(t, "Hello", session.Entries[0].Content)

	assert.Equal(t, EntryTypeAssistant, session.Entries[1].Type)
	assert.Equal(t, "Hi there!", session.Entries[1].Content)

	assert.Equal(t, EntryTypeUser, session.Entries[2].Type)
	assert.Equal(t, EntryTypeAssistant, session.Entries[3].Type)
}

func TestGeminiSessionHistory_GetSession_NotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)
	chatsDir := filepath.Join(projectDir, "chats")

	require.NoError(t, fs.MkdirAll(chatsDir, 0755))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	_, err := history.GetSession(workDir, "nonexistent")
	assert.Error(t, err)
}

func TestGeminiSessionHistory_GetSessionByPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	sessionPath := "/some/path/session.json"
	sessionContent := `{"messages":[{"role":"user","content":"Test","timestamp":"2024-01-15T10:00:00Z"}]}`

	require.NoError(t, fs.MkdirAll(filepath.Dir(sessionPath), 0755))
	require.NoError(t, afero.WriteFile(fs, sessionPath, []byte(sessionContent), 0644))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir("/test/home"),
	)

	session, err := history.GetSessionByPath(sessionPath)
	require.NoError(t, err)

	assert.Equal(t, "session.json", session.ID)
	assert.Len(t, session.Entries, 1)
}

func TestGeminiSessionHistory_GetCurrentSession(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)
	chatsDir := filepath.Join(projectDir, "chats")

	require.NoError(t, fs.MkdirAll(chatsDir, 0755))

	sessionContent := `{"messages":[{"role":"user","content":"Current","timestamp":"2024-01-15T10:00:00Z"}]}`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(chatsDir, "current.json"), []byte(sessionContent), 0644))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	session, err := history.GetCurrentSession(workDir)
	require.NoError(t, err)

	assert.Equal(t, "current.json", session.ID)
}

func TestGeminiSessionHistory_GetCurrentSession_NoSessions(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)
	chatsDir := filepath.Join(projectDir, "chats")

	require.NoError(t, fs.MkdirAll(chatsDir, 0755))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	_, err := history.GetCurrentSession(workDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no sessions found")
}

func TestGeminiSessionHistory_ConvertMessage(t *testing.T) {
	backend := NewGemini()
	history := NewGeminiSessionHistory(backend)

	tests := []struct {
		name     string
		msg      geminiMessage
		expected SessionEntryType
		content  string
		isNil    bool
	}{
		{
			name:     "user role",
			msg:      geminiMessage{Role: "user", Content: "Hello", Timestamp: "2024-01-15T10:00:00Z"},
			expected: EntryTypeUser,
			content:  "Hello",
		},
		{
			name:     "model role",
			msg:      geminiMessage{Role: "model", Content: "Response", Timestamp: "2024-01-15T10:00:00Z"},
			expected: EntryTypeAssistant,
			content:  "Response",
		},
		{
			name:     "assistant role",
			msg:      geminiMessage{Role: "assistant", Content: "Also works", Timestamp: "2024-01-15T10:00:00Z"},
			expected: EntryTypeAssistant,
			content:  "Also works",
		},
		{
			name:  "unknown role",
			msg:   geminiMessage{Role: "system", Content: "Ignored"},
			isNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := history.convertMessage(tt.msg)
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

func TestGeminiSessionHistory_ParseSession_MalformedJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	sessionPath := "/test/session.json"
	require.NoError(t, fs.MkdirAll(filepath.Dir(sessionPath), 0755))
	require.NoError(t, afero.WriteFile(fs, sessionPath, []byte("not json"), 0644))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir("/test/home"),
	)

	_, err := history.GetSessionByPath(sessionPath)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse session JSON")
}

func TestGeminiSessionHistory_FindProjectDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewGemini()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectDir := geminiProjectDir(homeDir, workDir)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	history := NewGeminiSessionHistory(backend,
		WithGeminiSessionFS(fs),
		WithGeminiSessionHomeDir(homeDir),
	)

	result, err := history.findProjectDir(workDir)
	require.NoError(t, err)
	assert.Equal(t, projectDir, result)
}

func TestGeminiSessionHistory_TranscriptPathFromHook(t *testing.T) {
	backend := NewGemini()
	history := NewGeminiSessionHistory(backend)

	// Gemini returns the path directly
	path := history.TranscriptPathFromHook("/work", "session-id", "/path/to/transcript")
	assert.Equal(t, "/path/to/transcript", path)
}
