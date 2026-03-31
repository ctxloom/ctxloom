package memory

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string", "", 0},
		{"short string", "test", 1}, // 4 chars / 4 = 1
		{"longer string", "hello world testing", 4}, // 19 chars / 4 = 4
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := estimateTokens(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCompactor_SessionToText(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	session := &backends.Session{
		ID: "test-session",
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "Hello"},
			{Type: backends.EntryTypeAssistant, Content: "Hi there!"},
			{Type: backends.EntryTypeToolUse, ToolName: "Read", ToolInput: []byte(`{"path":"/test"}`)},
			{Type: backends.EntryTypeToolResult, ToolName: "Read", ToolOutput: "file contents"},
			{Type: backends.EntryTypeSystem, Content: "System message"},
		},
	}

	text := c.sessionToText(session)

	assert.Contains(t, text, "## User\nHello")
	assert.Contains(t, text, "## Assistant\nHi there!")
	assert.Contains(t, text, "## Tool Call: Read")
	assert.Contains(t, text, "## Tool Result: Read")
	assert.Contains(t, text, "## System: System message")
}

func TestCompactor_SessionToText_TruncatesLargeContent(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	// Large tool input and output (> 500 chars)
	largeContent := make([]byte, 600)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeToolUse, ToolName: "Bash", ToolInput: largeContent},
			{Type: backends.EntryTypeToolResult, ToolName: "Bash", ToolOutput: string(largeContent)},
		},
	}

	text := c.sessionToText(session)

	// Should be truncated with "..."
	assert.Contains(t, text, "...")
}

func TestCompactor_SessionToText_ErrorFlag(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	session := &backends.Session{
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeToolResult, ToolName: "Bash", ToolOutput: "error message", IsError: true},
		},
	}

	text := c.sessionToText(session)

	assert.Contains(t, text, "[ERROR]")
}

func TestCompactor_ChunkText_SmallText(t *testing.T) {
	c := &Compactor{config: CompactionConfig{ChunkSize: DefaultChunkTokens}}

	smallText := "This is small text"
	chunks := c.chunkText(smallText, DefaultChunkTokens)

	assert.Len(t, chunks, 1)
	assert.Equal(t, smallText, chunks[0])
}

func TestCompactor_ChunkText_LargeText(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	// Create text larger than one chunk
	// DefaultChunkTokens * CharsPerToken = 8000 * 4 = 32000 chars
	largeText := ""
	for i := 0; i < 100; i++ {
		largeText += "## Section\nSome content here that goes on for a while.\n\n"
	}

	chunks := c.chunkText(largeText, 100) // 100 tokens = 400 chars

	assert.Greater(t, len(chunks), 1)
	// Each chunk should be non-empty
	for _, chunk := range chunks {
		assert.NotEmpty(t, chunk)
	}
}

func TestCompactor_ChunkText_BreaksAtHeaders(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	text := "## Section 1\nContent for section 1.\n\n## Section 2\nContent for section 2.\n\n## Section 3\nContent for section 3."

	// Use small chunk size to force splitting
	chunks := c.chunkText(text, 20) // 20 tokens = 80 chars

	// Should break at section boundaries when possible
	assert.Greater(t, len(chunks), 1)
}

func TestDistilledSession_Serialization(t *testing.T) {
	original := DistilledSession{
		SessionID:  "test-session",
		CreatedAt:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Content:    "This is the distilled content",
		TokenCount: 100,
	}

	data, err := yaml.Marshal(original)
	require.NoError(t, err)

	var loaded DistilledSession
	err = yaml.Unmarshal(data, &loaded)
	require.NoError(t, err)

	assert.Equal(t, original.SessionID, loaded.SessionID)
	assert.Equal(t, original.Content, loaded.Content)
	assert.Equal(t, original.TokenCount, loaded.TokenCount)
}

func TestSessionEssence_Serialization(t *testing.T) {
	original := SessionEssence{
		SessionID:   "test-session",
		CreatedAt:   time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Essence:     "Brief summary of the session",
		GeneratedAt: time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
	}

	data, err := yaml.Marshal(original)
	require.NoError(t, err)

	var loaded SessionEssence
	err = yaml.Unmarshal(data, &loaded)
	require.NoError(t, err)

	assert.Equal(t, original.SessionID, loaded.SessionID)
	assert.Equal(t, original.Essence, loaded.Essence)
}

func TestLoadDistilledSession(t *testing.T) {
	tmpDir := t.TempDir()
	distilledDir := filepath.Join(tmpDir, DistilledDir)
	require.NoError(t, os.MkdirAll(distilledDir, 0755))

	// Create a test distilled session file
	session := DistilledSession{
		SessionID:  "abc123",
		CreatedAt:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Content:    "Distilled content here",
		TokenCount: 50,
	}
	data, err := yaml.Marshal(session)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(distilledDir, "session-abc123.yaml"), data, 0644))

	// Load it
	loaded, err := LoadDistilledSession(tmpDir, "abc123")
	require.NoError(t, err)

	assert.Equal(t, "abc123", loaded.SessionID)
	assert.Equal(t, "Distilled content here", loaded.Content)
}

func TestLoadDistilledSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := LoadDistilledSession(tmpDir, "nonexistent")
	assert.Error(t, err)
}

func TestListDistilledSessions(t *testing.T) {
	tmpDir := t.TempDir()
	distilledDir := filepath.Join(tmpDir, DistilledDir)
	require.NoError(t, os.MkdirAll(distilledDir, 0755))

	// Create test files
	require.NoError(t, os.WriteFile(filepath.Join(distilledDir, "session-abc123.yaml"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(distilledDir, "session-def456.yaml"), []byte("{}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(distilledDir, "other.txt"), []byte("{}"), 0644)) // Should be ignored

	sessions, err := ListDistilledSessions(tmpDir)
	require.NoError(t, err)

	assert.Len(t, sessions, 2)
	assert.Contains(t, sessions, "abc123")
	assert.Contains(t, sessions, "def456")
}

func TestListDistilledSessions_Empty(t *testing.T) {
	tmpDir := t.TempDir()

	sessions, err := ListDistilledSessions(tmpDir)
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestLoadSessionEssence(t *testing.T) {
	tmpDir := t.TempDir()
	essencesDir := filepath.Join(tmpDir, EssencesDir)
	require.NoError(t, os.MkdirAll(essencesDir, 0755))

	essence := SessionEssence{
		SessionID:   "test123",
		CreatedAt:   time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		Essence:     "Test essence content",
		GeneratedAt: time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
	}
	data, err := yaml.Marshal(essence)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(essencesDir, "session-test123.yaml"), data, 0644))

	loaded, err := LoadSessionEssence(tmpDir, "test123")
	require.NoError(t, err)

	assert.Equal(t, "test123", loaded.SessionID)
	assert.Equal(t, "Test essence content", loaded.Essence)
}

func TestSaveSessionEssence(t *testing.T) {
	tmpDir := t.TempDir()

	essence := &SessionEssence{
		SessionID:   "saved123",
		CreatedAt:   time.Now(),
		Essence:     "Saved essence",
		GeneratedAt: time.Now(),
	}

	err := SaveSessionEssence(tmpDir, essence)
	require.NoError(t, err)

	// Verify file was created
	path := filepath.Join(tmpDir, EssencesDir, "session-saved123.yaml")
	_, err = os.Stat(path)
	require.NoError(t, err)

	// Load and verify
	loaded, err := LoadSessionEssence(tmpDir, "saved123")
	require.NoError(t, err)
	assert.Equal(t, "Saved essence", loaded.Essence)
}

func TestCompactionConfig_Defaults(t *testing.T) {
	// Test that NewCompactor sets defaults
	// Note: This will fail without a registered backend, so we just test the config struct
	config := CompactionConfig{
		WorkDir: "/test",
	}

	assert.Empty(t, config.Plugin)
	assert.Empty(t, config.Backend)
	assert.Zero(t, config.ChunkSize)
}

func TestCompactor_DistillChunk_WithMockClient(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock client that returns distilled content
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte("Distilled: key decisions and outcomes"))
			return 0, nil
		},
	}

	c := &Compactor{
		config: CompactionConfig{
			Plugin:    "test-plugin",
			OutputDir: tmpDir,
		},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	result, err := c.distillChunk(context.Background(), "Original session content", 1, 3)
	require.NoError(t, err)

	assert.Equal(t, "Distilled: key decisions and outcomes", result)
	assert.Equal(t, 1, mockClient.RunCalls)
}

func TestCompactor_DistillChunk_ClientError(t *testing.T) {
	// Create a mock client that returns an error
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			return 0, errors.New("connection failed")
		},
	}

	c := &Compactor{
		config: CompactionConfig{
			Plugin: "test-plugin",
		},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	_, err := c.distillChunk(context.Background(), "content", 1, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")
}

func TestCompactor_DistillChunk_NonZeroExit(t *testing.T) {
	// Create a mock client that returns non-zero exit code
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			_, _ = stderr.Write([]byte("LLM error"))
			return 1, nil
		},
	}

	c := &Compactor{
		config: CompactionConfig{
			Plugin: "test-plugin",
		},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	_, err := c.distillChunk(context.Background(), "content", 1, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 1")
}

func TestGenerateSessionEssence_WithMockClient(t *testing.T) {
	tmpDir := t.TempDir()

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte("Brief session summary for testing."))
			return 0, nil
		},
	}

	session := &backends.Session{
		ID:        "test-essence-session",
		StartTime: time.Now(),
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "What's the weather?"},
			{Type: backends.EntryTypeAssistant, Content: "I don't have access to weather data."},
		},
	}

	config := EssenceConfig{
		Plugin:        "test-plugin",
		Model:         "fast",
		MemoryDir:     tmpDir,
		ClientFactory: pb.MockClientFactory(mockClient),
	}

	essence, err := GenerateSessionEssence(context.Background(), session, config)
	require.NoError(t, err)

	assert.Equal(t, "test-essence-session", essence.SessionID)
	assert.Equal(t, "Brief session summary for testing.", essence.Essence)
	assert.Equal(t, 1, mockClient.RunCalls)
}

func TestGenerateSessionEssence_UsesCache(t *testing.T) {
	tmpDir := t.TempDir()

	// Pre-populate cache
	cachedEssence := &SessionEssence{
		SessionID:   "cached-session",
		CreatedAt:   time.Now(),
		Essence:     "Cached essence content",
		GeneratedAt: time.Now(),
	}
	require.NoError(t, SaveSessionEssence(tmpDir, cachedEssence))

	// Mock client that should NOT be called
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			t.Fatal("Client should not be called when cache exists")
			return 0, nil
		},
	}

	session := &backends.Session{
		ID: "cached-session",
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "test"},
		},
	}

	config := EssenceConfig{
		Plugin:        "test-plugin",
		MemoryDir:     tmpDir,
		ClientFactory: pb.MockClientFactory(mockClient),
	}

	essence, err := GenerateSessionEssence(context.Background(), session, config)
	require.NoError(t, err)

	assert.Equal(t, "Cached essence content", essence.Essence)
	assert.Equal(t, 0, mockClient.RunCalls)
}

func TestGenerateSessionEssence_EmptySession(t *testing.T) {
	tmpDir := t.TempDir()

	mockClient := &pb.MockClient{}

	session := &backends.Session{
		ID:        "empty-session",
		StartTime: time.Now(),
		Entries:   []backends.SessionEntry{},
	}

	config := EssenceConfig{
		Plugin:        "test-plugin",
		MemoryDir:     tmpDir,
		ClientFactory: pb.MockClientFactory(mockClient),
	}

	essence, err := GenerateSessionEssence(context.Background(), session, config)
	require.NoError(t, err)

	assert.Equal(t, "(empty session)", essence.Essence)
	assert.Equal(t, 0, mockClient.RunCalls) // Client not called for empty sessions
}

func TestMockClientFactory(t *testing.T) {
	mock := &pb.MockClient{}
	factory := pb.MockClientFactory(mock)

	client, err := factory("any-backend", 0)
	require.NoError(t, err)

	assert.Same(t, mock, client)
}

// mockBackend implements backends.Backend for testing compactor.
type mockBackend struct {
	history backends.SessionHistory
}

func (m *mockBackend) Name() string                                       { return "mock-test" }
func (m *mockBackend) Version() string                                    { return "1.0.0" }
func (m *mockBackend) SupportedModes() []backends.ExecutionMode           { return []backends.ExecutionMode{backends.ModeInteractive, backends.ModeOneshot} }
func (m *mockBackend) Lifecycle() backends.LifecycleHandler               { return nil }
func (m *mockBackend) Skills() backends.SkillRegistry                     { return nil }
func (m *mockBackend) Context() backends.ContextProvider                  { return nil }
func (m *mockBackend) MCP() backends.MCPManager                           { return nil }
func (m *mockBackend) History() backends.SessionHistory                   { return m.history }
func (m *mockBackend) WorkDir() string                                    { return "" }
func (m *mockBackend) SetWorkDir(string)                                  {}
func (m *mockBackend) Setup(context.Context, *backends.SetupRequest) error { return nil }
func (m *mockBackend) Execute(context.Context, *backends.ExecuteRequest, io.Writer, io.Writer) (*backends.ExecuteResult, error) {
	return &backends.ExecuteResult{ExitCode: 0}, nil
}
func (m *mockBackend) Cleanup(context.Context) error { return nil }

// mockSessionHistory implements backends.SessionHistory for testing.
type mockSessionHistory struct {
	currentSession *backends.Session
	sessions       map[string]*backends.Session
	sessionList    []backends.SessionMeta
}

func (m *mockSessionHistory) GetCurrentSession(workDir string) (*backends.Session, error) {
	if m.currentSession == nil {
		return nil, errors.New("no current session")
	}
	return m.currentSession, nil
}

func (m *mockSessionHistory) ListSessions(workDir string) ([]backends.SessionMeta, error) {
	return m.sessionList, nil
}

func (m *mockSessionHistory) GetSession(workDir string, sessionID string) (*backends.Session, error) {
	if s, ok := m.sessions[sessionID]; ok {
		return s, nil
	}
	return nil, errors.New("session not found")
}

func (m *mockSessionHistory) GetSessionByPath(path string) (*backends.Session, error) {
	return nil, errors.New("not implemented")
}

func (m *mockSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

func (m *mockSessionHistory) RegisterSession(workDir string, pid int, transcriptPath string) error {
	return nil
}

func (m *mockSessionHistory) GetPreviousSession(workDir string, pid int) (*backends.Session, error) {
	return nil, errors.New("not implemented")
}

func TestNewCompactor_WithBackendOverride(t *testing.T) {
	mockHistory := &mockSessionHistory{}
	mockBe := &mockBackend{history: mockHistory}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		WorkDir:         "/test",
	})
	require.NoError(t, err)
	assert.NotNil(t, compactor)
	assert.Equal(t, mockBe, compactor.backend)
}

func TestNewCompactor_SetsDefaults(t *testing.T) {
	mockBe := &mockBackend{history: &mockSessionHistory{}}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
	})
	require.NoError(t, err)

	assert.Equal(t, DefaultChunkTokens, compactor.config.ChunkSize)
	assert.Equal(t, "claude-code", compactor.config.Backend)
	assert.Equal(t, "claude-code", compactor.config.Plugin)
	assert.NotNil(t, compactor.clientFactory)
}

func TestCompact_NoHistorySupport(t *testing.T) {
	mockBe := &mockBackend{history: nil}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "does not support session history")
}

func TestCompact_NoSession(t *testing.T) {
	mockHistory := &mockSessionHistory{currentSession: nil}
	mockBe := &mockBackend{history: mockHistory}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	assert.Error(t, err)
}

func TestCompact_EmptySession(t *testing.T) {
	mockHistory := &mockSessionHistory{
		currentSession: &backends.Session{
			ID:      "empty-session",
			Entries: []backends.SessionEntry{},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "has no entries")
}

func TestCompact_WithMockClient(t *testing.T) {
	tmpDir := t.TempDir()

	mockHistory := &mockSessionHistory{
		currentSession: &backends.Session{
			ID: "test-compact-session",
			Entries: []backends.SessionEntry{
				{Type: backends.EntryTypeUser, Content: "Hello, how are you?"},
				{Type: backends.EntryTypeAssistant, Content: "I'm doing well, thank you!"},
			},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte("Distilled: User greeted assistant, assistant responded positively."))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "test-compact-session", result.SessionID)
	assert.Equal(t, 1, result.ChunksCreated)
	assert.NotEmpty(t, result.DistilledPath)
	assert.Greater(t, result.TotalTokensIn, 0)
	assert.Greater(t, result.TotalTokensOut, 0)

	// Verify file was created
	_, err = os.Stat(result.DistilledPath)
	require.NoError(t, err)
}

func TestCompact_BySessionID(t *testing.T) {
	tmpDir := t.TempDir()

	targetSession := &backends.Session{
		ID: "specific-session",
		Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "Specific request"},
		},
	}
	mockHistory := &mockSessionHistory{
		sessions: map[string]*backends.Session{
			"specific-session": targetSession,
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte("Distilled content"))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
		SessionID:       "specific-session",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "specific-session", result.SessionID)
}

func TestSaveDistilled(t *testing.T) {
	tmpDir := t.TempDir()

	c := &Compactor{
		config: CompactionConfig{
			OutputDir: tmpDir,
		},
	}

	path, err := c.saveDistilled("test-session-123", "Distilled content here")
	require.NoError(t, err)

	assert.Contains(t, path, "test-session-123")

	// Verify file contents
	loaded, err := LoadDistilledSession(tmpDir, "test-session-123")
	require.NoError(t, err)
	assert.Equal(t, "Distilled content here", loaded.Content)
	assert.Equal(t, "test-session-123", loaded.SessionID)
}
