package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadSessionLog(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	// Create a test session log
	logContent := `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"user","content":"Hello"}
{"ts":"2024-01-15T10:00:01Z","session":"test","type":"assistant","content":"Hi there!"}
{"ts":"2024-01-15T10:00:02Z","session":"test","type":"tool_call","tool_call":{"name":"Bash","args":{}}}
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-test123.jsonl"), []byte(logContent), 0644))

	entries, err := LoadSessionLog(tmpDir, "test123")
	require.NoError(t, err)

	assert.Len(t, entries, 3)
	assert.Equal(t, EntryTypeUser, entries[0].Type)
	assert.Equal(t, "Hello", entries[0].Content)
	assert.Equal(t, EntryTypeAssistant, entries[1].Type)
	assert.Equal(t, "Hi there!", entries[1].Content)
	assert.Equal(t, EntryTypeToolCall, entries[2].Type)
}

func TestLoadSessionLog_SkipsMalformed(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	// Mix of valid and invalid lines
	logContent := `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"user","content":"Valid"}
not valid json
{"ts":"2024-01-15T10:00:01Z","session":"test","type":"assistant","content":"Also valid"}
`
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-mixed.jsonl"), []byte(logContent), 0644))

	entries, err := LoadSessionLog(tmpDir, "mixed")
	require.NoError(t, err)

	// Should have 2 valid entries
	assert.Len(t, entries, 2)
}

func TestLoadSessionLog_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := LoadSessionLog(tmpDir, "nonexistent")
	assert.Error(t, err)
}

func TestLoadSessionLog_EmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-empty.jsonl"), []byte(""), 0644))

	entries, err := LoadSessionLog(tmpDir, "empty")
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestLoadSessionMeta(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	meta := SessionMeta{
		SessionID:        "test123",
		ProjectPath:      "/test/project",
		Profile:          "default",
		EntryCount:       100,
		TokensEstimate:   5000,
		CompactionStatus: "pending",
	}
	data, err := yaml.Marshal(meta)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-test123.meta.yaml"), data, 0644))

	loaded, err := LoadSessionMeta(tmpDir, "test123")
	require.NoError(t, err)

	assert.Equal(t, "test123", loaded.SessionID)
	assert.Equal(t, "/test/project", loaded.ProjectPath)
	assert.Equal(t, 100, loaded.EntryCount)
}

func TestLoadSessionMeta_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := LoadSessionMeta(tmpDir, "nonexistent")
	assert.Error(t, err)
}

func TestLoadSessionMeta_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-bad.meta.yaml"), []byte("not: valid: yaml: {{"), 0644))

	_, err := LoadSessionMeta(tmpDir, "bad")
	assert.Error(t, err)
}

func TestListSessions(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	// Create test session files
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-abc123.jsonl"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-def456.jsonl"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "session-abc123.meta.yaml"), []byte("{}"), 0644)) // Should be ignored
	require.NoError(t, os.WriteFile(filepath.Join(sessionsDir, "other.txt"), []byte(""), 0644))                  // Should be ignored
	require.NoError(t, os.Mkdir(filepath.Join(sessionsDir, "subdir"), 0755))                                     // Should be ignored

	sessions, err := ListSessions(tmpDir)
	require.NoError(t, err)

	assert.Len(t, sessions, 2)
	assert.Contains(t, sessions, "abc123")
	assert.Contains(t, sessions, "def456")
}

func TestListSessions_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsDir := filepath.Join(tmpDir, SessionsDir)
	require.NoError(t, os.MkdirAll(sessionsDir, 0755))

	sessions, err := ListSessions(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestListSessions_NoDir(t *testing.T) {
	tmpDir := t.TempDir()

	sessions, err := ListSessions(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, sessions) // Returns nil, nil when dir doesn't exist
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "sessions", SessionsDir)
	assert.Equal(t, "distilled", DistilledDir)
	assert.Equal(t, "essences", EssencesDir)
}
