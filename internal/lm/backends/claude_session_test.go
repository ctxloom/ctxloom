package backends

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Claude Session History Tests
// =============================================================================

func TestClaudeSessionHistory_New(t *testing.T) {
	backend := NewClaudeCode()
	history := NewClaudeSessionHistory(backend)

	assert.NotNil(t, history)
	assert.Equal(t, backend, history.backend)
	assert.NotNil(t, history.fs)
}

func TestClaudeSessionHistory_WithOptions(t *testing.T) {
	backend := NewClaudeCode()
	fs := afero.NewMemMapFs()

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir("/test/home"),
	)

	assert.NotNil(t, history)
	assert.Equal(t, fs, history.fs)
	assert.Equal(t, "/test/home", history.homeDir)
}

func TestClaudeSessionHistory_ListSessions(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	// Setup test directory structure
	// Claude uses: ~/.claude/projects/-<path-with-dashes>/
	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project" // Claude converts /test/project -> -test-project
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	// Create session files with different times
	session1 := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Hello"}}
{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","message":{"content":"Hi there!"}}`

	session2 := `{"type":"user","timestamp":"2024-01-15T11:00:00Z","message":{"content":"Second session"}}
{"type":"assistant","timestamp":"2024-01-15T11:00:01Z","message":{"content":"Response"}}`

	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, "session1.jsonl"), []byte(session1), 0644))
	time.Sleep(10 * time.Millisecond) // Ensure different mod times
	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, "session2.jsonl"), []byte(session2), 0644))

	// Also create a non-session file that should be ignored
	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, "config.json"), []byte("{}"), 0644))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	sessions, err := history.ListSessions(workDir)
	require.NoError(t, err)

	assert.Len(t, sessions, 2)
	// Sessions should be sorted by time, most recent first
	assert.Contains(t, []string{"session1", "session2"}, sessions[0].ID)
	assert.Contains(t, []string{"session1", "session2"}, sessions[1].ID)
}

func TestClaudeSessionHistory_ListSessions_Empty(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	sessions, err := history.ListSessions(workDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestClaudeSessionHistory_ListSessions_ProjectNotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir("/test/home"),
	)

	_, err := history.ListSessions("/test/project")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "project directory not found")
}

func TestClaudeSessionHistory_GetSession(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	sessionContent := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Hello"}}
{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","message":{"content":"Hi there!"}}
{"type":"tool_use","timestamp":"2024-01-15T10:00:02Z","name":"Read","input":{"path":"/test"}}
{"type":"tool_result","timestamp":"2024-01-15T10:00:03Z","name":"Read","output":"file contents"}`

	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, "test-session.jsonl"), []byte(sessionContent), 0644))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	session, err := history.GetSession(workDir, "test-session")
	require.NoError(t, err)

	assert.Equal(t, "test-session", session.ID)
	assert.Len(t, session.Entries, 4)

	// Verify entry types
	assert.Equal(t, EntryTypeUser, session.Entries[0].Type)
	assert.Equal(t, "Hello", session.Entries[0].Content)

	assert.Equal(t, EntryTypeAssistant, session.Entries[1].Type)
	assert.Equal(t, "Hi there!", session.Entries[1].Content)

	assert.Equal(t, EntryTypeToolUse, session.Entries[2].Type)
	assert.Equal(t, "Read", session.Entries[2].ToolName)

	assert.Equal(t, EntryTypeToolResult, session.Entries[3].Type)
	assert.Equal(t, "file contents", session.Entries[3].ToolOutput)

	// Verify timestamps
	assert.False(t, session.StartTime.IsZero())
	assert.False(t, session.EndTime.IsZero())
}

func TestClaudeSessionHistory_GetSession_NotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	_, err := history.GetSession(workDir, "nonexistent")
	assert.Error(t, err)
}

func TestClaudeSessionHistory_GetSessionByPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	sessionPath := "/some/path/session.jsonl"
	sessionContent := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Test"}}`

	require.NoError(t, fs.MkdirAll(filepath.Dir(sessionPath), 0755))
	require.NoError(t, afero.WriteFile(fs, sessionPath, []byte(sessionContent), 0644))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir("/test/home"),
	)

	session, err := history.GetSessionByPath(sessionPath)
	require.NoError(t, err)

	assert.Equal(t, "session", session.ID)
	assert.Len(t, session.Entries, 1)
}

func TestClaudeSessionHistory_GetCurrentSession(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	sessionContent := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Current"}}`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(projectDir, "current.jsonl"), []byte(sessionContent), 0644))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	session, err := history.GetCurrentSession(workDir)
	require.NoError(t, err)

	assert.Equal(t, "current", session.ID)
}

func TestClaudeSessionHistory_GetCurrentSession_NoSessions(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	_, err := history.GetCurrentSession(workDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no sessions found")
}

func TestClaudeSessionHistory_ParseEntry_UserMessage(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()
	history := NewClaudeSessionHistory(backend, WithClaudeSessionFS(fs))

	tests := []struct {
		name     string
		input    string
		expected SessionEntryType
		content  string
	}{
		{
			name:     "user type",
			input:    `{"type":"user","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Hello"}}`,
			expected: EntryTypeUser,
			content:  "Hello",
		},
		{
			name:     "human type",
			input:    `{"type":"human","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Hello human"}}`,
			expected: EntryTypeUser,
			content:  "Hello human",
		},
		{
			name:     "assistant type",
			input:    `{"type":"assistant","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Response"}}`,
			expected: EntryTypeAssistant,
			content:  "Response",
		},
		{
			name:     "tool_use type",
			input:    `{"type":"tool_use","timestamp":"2024-01-15T10:00:00Z","name":"Bash","input":{"command":"ls"}}`,
			expected: EntryTypeToolUse,
		},
		{
			name:     "tool_result type",
			input:    `{"type":"tool_result","timestamp":"2024-01-15T10:00:00Z","name":"Bash","output":"file.txt"}`,
			expected: EntryTypeToolResult,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := history.parseEntry([]byte(tt.input))
			require.NoError(t, err)
			assert.Equal(t, tt.expected, entry.Type)
			if tt.content != "" {
				assert.Equal(t, tt.content, entry.Content)
			}
		})
	}
}

func TestClaudeSessionHistory_ParseEntry_MalformedJSON(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()
	history := NewClaudeSessionHistory(backend, WithClaudeSessionFS(fs))

	_, err := history.parseEntry([]byte("not json"))
	assert.Error(t, err)
}

func TestClaudeSessionHistory_ParseSession_SkipsMalformed(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	sessionPath := "/test/session.jsonl"
	// Mix of valid and invalid lines
	sessionContent := `{"type":"user","timestamp":"2024-01-15T10:00:00Z","message":{"content":"Valid"}}
not valid json
{"type":"assistant","timestamp":"2024-01-15T10:00:01Z","message":{"content":"Also valid"}}`

	require.NoError(t, fs.MkdirAll(filepath.Dir(sessionPath), 0755))
	require.NoError(t, afero.WriteFile(fs, sessionPath, []byte(sessionContent), 0644))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir("/test/home"),
	)

	session, err := history.GetSessionByPath(sessionPath)
	require.NoError(t, err)

	// Should have 2 entries, skipping the malformed one
	assert.Len(t, session.Entries, 2)
}

func TestClaudeSessionHistory_FindProjectDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	result, err := history.findProjectDir(workDir)
	require.NoError(t, err)
	assert.Equal(t, projectDir, result)
}

func TestClaudeSessionHistory_FindSessionFile(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)
	sessionFile := filepath.Join(projectDir, "session.jsonl")

	require.NoError(t, fs.MkdirAll(projectDir, 0755))
	require.NoError(t, afero.WriteFile(fs, sessionFile, []byte("{}"), 0644))

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	result, err := history.findSessionFile(workDir)
	require.NoError(t, err)
	assert.Equal(t, sessionFile, result)
}

func TestClaudeSessionHistory_FindSessionFile_NotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	backend := NewClaudeCode()

	homeDir := "/test/home"
	workDir := "/test/project"
	projectName := "-test-project"
	projectDir := filepath.Join(homeDir, ".claude", "projects", projectName)

	require.NoError(t, fs.MkdirAll(projectDir, 0755))
	// Don't create session.jsonl

	history := NewClaudeSessionHistory(backend,
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	_, err := history.findSessionFile(workDir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session file not found")
}
