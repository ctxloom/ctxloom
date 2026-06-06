package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string", "", 0},
		{"short string", "test", 1},                 // 4 chars / 4 = 1
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

	text := c.sessionToText(session, nil)

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

	text := c.sessionToText(session, nil)

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

	text := c.sessionToText(session, nil)

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

func TestDistilledSession_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	c := &Compactor{config: CompactionConfig{OutputDir: tmpDir}}
	path, err := c.saveDistilled("round-trip", "## Summary\nDistilled body.", distilledMeta{
		EntryCount: 12,
		TokensIn:   2000,
		TokensOut:  300,
		PlanBlocks: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(tmpDir, "round-trip.md"), path)

	loaded, err := LoadDistilledSession(tmpDir, "round-trip")
	require.NoError(t, err)
	assert.Equal(t, "round-trip", loaded.SessionID)
	assert.Equal(t, 12, loaded.EntryCount)
	assert.Equal(t, 2000, loaded.TokensIn)
	assert.Equal(t, 300, loaded.TokensOut)
	assert.Equal(t, 2, loaded.PlanBlocks)
	assert.False(t, loaded.DistilledAt.IsZero())
	assert.Contains(t, loaded.Body, "## Summary")
	assert.Contains(t, loaded.Body, "Distilled body.")
}

func TestLoadDistilledSession(t *testing.T) {
	tmpDir := t.TempDir()

	frontmatter := "---\n" +
		"session_id: abc123\n" +
		"distilled_at: 2024-01-15T10:00:00Z\n" +
		"entry_count: 8\n" +
		"plan_blocks: 0\n" +
		"---\n\n" +
		"# Session summary\n\nDistilled content here\n"
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "abc123.md"), []byte(frontmatter), 0644))

	loaded, err := LoadDistilledSession(tmpDir, "abc123")
	require.NoError(t, err)

	assert.Equal(t, "abc123", loaded.SessionID)
	assert.Equal(t, 8, loaded.EntryCount)
	assert.Contains(t, loaded.Body, "Distilled content here")
}

func TestLoadDistilledSession_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := LoadDistilledSession(tmpDir, "nonexistent")
	assert.Error(t, err)
}

func TestListDistilledSessions(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "abc123.md"), []byte("---\nsession_id: abc123\n---\n\n# x\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "def456.md"), []byte("---\nsession_id: def456\n---\n\n# x\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "other.txt"), []byte("ignored"), 0644))

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

func TestCompactionConfig_Defaults(t *testing.T) {
	// Test that NewCompactor sets defaults
	// Note: This will fail without a registered backend, so we just test the config struct
	config := CompactionConfig{
		WorkDir: "/test",
	}

	assert.Empty(t, config.LLM)
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
			LLM:       "test-plugin",
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
			LLM: "test-plugin",
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
			LLM: "test-plugin",
		},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	_, err := c.distillChunk(context.Background(), "content", 1, 1)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 1")
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

func (m *mockBackend) Name() string    { return "mock-test" }
func (m *mockBackend) Version() string { return "1.0.0" }
func (m *mockBackend) SupportedModes() []backends.ExecutionMode {
	return []backends.ExecutionMode{backends.ModeInteractive, backends.ModeOneshot}
}
func (m *mockBackend) Lifecycle() backends.LifecycleHandler                { return nil }
func (m *mockBackend) Skills() backends.SkillRegistry                      { return nil }
func (m *mockBackend) Context() backends.ContextProvider                   { return nil }
func (m *mockBackend) MCP() backends.MCPManager                            { return nil }
func (m *mockBackend) History() backends.SessionHistory                    { return m.history }
func (m *mockBackend) WorkDir() string                                     { return "" }
func (m *mockBackend) SetWorkDir(string)                                   {}
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

func (m *mockSessionHistory) GetPreviousSession(workDir string) (*backends.Session, error) {
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
	assert.NotNil(t, compactor.source, "BackendOverride history must be adapted to a SessionSource")
}

func TestNewCompactor_SetsDefaults(t *testing.T) {
	mockBe := &mockBackend{history: &mockSessionHistory{}}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
	})
	require.NoError(t, err)

	assert.Equal(t, DefaultChunkTokens, compactor.config.ChunkSize)
	assert.Equal(t, "claude-code", compactor.config.Backend)
	assert.Equal(t, "claude-code", compactor.config.LLM)
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
	testsupport.Isolate(t)
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

func TestCompact_PreservesPlansVerbatim(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	planBody := "1. design the schema\n2. migrate data with backfill\n3. verify with smoke tests"
	toolInput, err := json.Marshal(map[string]string{"plan": planBody})
	require.NoError(t, err)

	mockHistory := &mockSessionHistory{
		currentSession: &backends.Session{
			ID: "plan-survival",
			Entries: []backends.SessionEntry{
				{Type: backends.EntryTypeUser, Content: "make a plan"},
				{
					Type:      backends.EntryTypeToolUse,
					ToolName:  "ExitPlanMode",
					ToolInput: toolInput,
				},
				{Type: backends.EntryTypeAssistant, Content: "plan ready"},
			},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	// Capture the prompt the LLM sees so we can assert plan content was
	// excised in favor of a placeholder.
	var sawLLMInput string
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, error) {
			sawLLMInput = req.Prompt.Content
			_, _ = stdout.Write([]byte("### Summary\nUser asked for a plan; see plan-block #1."))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.NoError(t, err)

	// The LLM should have seen the placeholder, never the plan body.
	assert.Contains(t, sawLLMInput, "[plan-block #1 — ExitPlanMode, preserved below]")
	assert.NotContains(t, sawLLMInput, planBody)

	loaded, err := LoadDistilledSession(tmpDir, "plan-survival")
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.PlanBlocks)
	assert.Contains(t, loaded.Body, "## Preserved plans")
	assert.Contains(t, loaded.Body, planBody)
}

func TestCompact_BySessionID(t *testing.T) {
	testsupport.Isolate(t)
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

// =============================================================================
// parseLLMFrontmatter — Phase 3.5.2
// =============================================================================

func TestParseLLMFrontmatter_HappyPath(t *testing.T) {
	in := `---
summary: Designed bundle review on startup; landed PR f1262a4
---

### Open Items
- thing one
- thing two
`
	summary, body, ok := parseLLMFrontmatter(in)
	assert.True(t, ok)
	assert.Equal(t, "Designed bundle review on startup; landed PR f1262a4", summary)
	assert.Contains(t, body, "### Open Items")
	assert.NotContains(t, body, "summary:")
}

func TestParseLLMFrontmatter_NoLeadingDashes(t *testing.T) {
	in := "### Open Items\n- thing\n"
	summary, body, ok := parseLLMFrontmatter(in)
	assert.False(t, ok)
	assert.Empty(t, summary)
	assert.Equal(t, in, body, "body should pass through unchanged on parse failure")
}

func TestParseLLMFrontmatter_NoClosingDashes(t *testing.T) {
	in := "---\nsummary: x\n# missing close\n"
	_, _, ok := parseLLMFrontmatter(in)
	assert.False(t, ok)
}

func TestParseLLMFrontmatter_MalformedYAML(t *testing.T) {
	in := "---\nsummary: [unterminated\n---\nbody\n"
	_, _, ok := parseLLMFrontmatter(in)
	assert.False(t, ok)
}

func TestParseLLMFrontmatter_TruncatesLongSummary(t *testing.T) {
	long := "this summary is exactly eighty characters long without trailing punctuation."
	require.Less(t, len(long), 80, "fixture sanity")
	overlong := long + " plus extra content that should be chopped off here"
	in := "---\nsummary: " + overlong + "\n---\n\nbody\n"
	summary, _, ok := parseLLMFrontmatter(in)
	assert.True(t, ok)
	assert.LessOrEqual(t, len(summary), 80)
}

func TestParseLLMFrontmatter_FirstLineOnlyIfMultiline(t *testing.T) {
	in := "---\nsummary: |\n  line one\n  line two\n---\n\nbody\n"
	summary, _, ok := parseLLMFrontmatter(in)
	assert.True(t, ok)
	assert.NotContains(t, summary, "\n")
	assert.Equal(t, "line one", summary)
}

func TestParseLLMFrontmatter_StripsLeadingWhitespace(t *testing.T) {
	// Some Configs put a stray blank line or whitespace before the ---. Tolerate it.
	in := "\n\n  \n---\nsummary: ok\n---\nbody\n"
	summary, _, ok := parseLLMFrontmatter(in)
	assert.True(t, ok)
	assert.Equal(t, "ok", summary)
}

func TestParseLLMFrontmatter_EmptySummaryStillSucceeds(t *testing.T) {
	in := "---\nsummary: \n---\nbody\n"
	summary, body, ok := parseLLMFrontmatter(in)
	assert.True(t, ok)
	assert.Empty(t, summary)
	assert.Equal(t, "body\n", body)
}

// =============================================================================
// deriveSummary — body fallback so a distilled session is never "(no summary)"
// =============================================================================

func TestDeriveSummary(t *testing.T) {
	longLine := strings.Repeat("x", 120)
	tests := []struct {
		name        string
		frontmatter string
		body        string
		expected    string
	}{
		{
			name:        "frontmatter wins when present",
			frontmatter: "From the frontmatter",
			body:        "# Heading\n\nFirst body line",
			expected:    "From the frontmatter",
		},
		{
			name:        "falls back to first prose line",
			frontmatter: "",
			body:        "# Session summary\n\nSee you! Setup is live.",
			expected:    "See you! Setup is live.",
		},
		{
			name:        "skips headings and blank lines",
			frontmatter: "",
			body:        "## Top\n\n###  Sub\n\n   \nActual content here",
			expected:    "Actual content here",
		},
		{
			name:        "caps fallback at 80 bytes",
			frontmatter: "",
			body:        longLine,
			expected:    longLine[:80],
		},
		{
			name:        "empty when body has no prose",
			frontmatter: "",
			body:        "# Only\n## Headings\n   \n",
			expected:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, deriveSummary(tt.frontmatter, tt.body))
		})
	}
}
