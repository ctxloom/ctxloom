package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/shared/agent"
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

	session := &agent.Session{
		ID: "test-session",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: "Hello"},
			{Type: agent.EntryTypeAssistant, Content: "Hi there!"},
			{Type: agent.EntryTypeToolUse, ToolName: "Read", ToolInput: []byte(`{"path":"/test"}`)},
			{Type: agent.EntryTypeToolResult, ToolName: "Read", ToolOutput: "file contents"},
			{Type: agent.EntryTypeSystem, Content: "System message"},
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

	session := &agent.Session{
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeToolUse, ToolName: "Bash", ToolInput: largeContent},
			{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: string(largeContent)},
		},
	}

	text := c.sessionToText(session)

	// Should be truncated with "..."
	assert.Contains(t, text, "...")
}

func TestCompactor_SessionToText_ErrorFlag(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	session := &agent.Session{
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeToolResult, ToolName: "Bash", ToolOutput: "error message", IsError: true},
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

// TestCompactor_ChunkText_UTF8RuneBoundaries pins the rune-boundary backoff:
// chunk boundaries are byte offsets, and a mid-rune cut produced invalid UTF-8
// that fails proto3 string marshaling downstream, silently turning the chunk
// into a failure marker (content loss).
func TestCompactor_ChunkText_UTF8RuneBoundaries(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	t.Run("no-overlap regime reconstructs exactly", func(t *testing.T) {
		// 9-byte repeating unit (3+4+2 bytes) so the 100-byte target boundary
		// (25 tokens * 4 chars) always lands mid-rune.
		text := strings.Repeat("界😀é", 120) // 1080 bytes
		chunks := c.chunkText(text, 25)     // 100-byte chunks; overlap (2000) > chunk → no overlap
		require.Greater(t, len(chunks), 1)
		for i, chunk := range chunks {
			assert.True(t, utf8.ValidString(chunk), "chunk %d is not valid UTF-8: %q", i, chunk)
		}
		assert.Equal(t, text, strings.Join(chunks, ""),
			"without overlap, chunks must reconstruct the input exactly — no content lost or duplicated")
	})

	t.Run("overlap regime stays valid and covers everything", func(t *testing.T) {
		// Chunks larger than the 2000-byte overlap exercise the advance cut.
		// Counter prefixes make every chunk unique so coverage is checkable.
		var b strings.Builder
		for i := 0; b.Len() < 12000; i++ {
			fmt.Fprintf(&b, "%d界😀é", i)
		}
		text := b.String()
		chunks := c.chunkText(text, 600) // 2400-byte chunks, 2000-byte overlap
		require.Greater(t, len(chunks), 1)

		covered := 0 // end of the covered prefix of text
		for i, chunk := range chunks {
			require.True(t, utf8.ValidString(chunk), "chunk %d is not valid UTF-8", i)
			idx := strings.Index(text, chunk)
			require.GreaterOrEqual(t, idx, 0, "chunk %d must be a substring of the input", i)
			require.LessOrEqual(t, idx, covered, "chunk %d starts past the covered prefix — content lost", i)
			if end := idx + len(chunk); end > covered {
				covered = end
			}
		}
		assert.Equal(t, len(text), covered, "chunks must cover the full input")
	})
}

// TestCompactor_ChunkText_NoTrailingOverlapDuplicate pins the loop exit: once
// the final chunk reaches the end of the text, the loop must stop. Advancing
// by chunkEnd-overlap instead re-entered the loop with the pure-overlap tail
// and emitted it again as a standalone chunk — one wasted LLM distill call of
// duplicated content per compaction.
func TestCompactor_ChunkText_NoTrailingOverlapDuplicate(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	text := strings.TrimSpace(strings.Repeat("alpha beta gamma delta ", 60)) // ~1380 chars
	chunks := c.chunkText(text, 100)                                         // 400-char chunks, 200-char overlap

	require.Greater(t, len(chunks), 1)
	for i := 1; i < len(chunks); i++ {
		assert.False(t, strings.HasSuffix(chunks[i-1], chunks[i]),
			"chunk %d is a pure-overlap duplicate of the tail of chunk %d", i, i-1)
	}
}

func TestDistilledSession_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()

	c := &Compactor{config: CompactionConfig{OutputDir: tmpDir}}
	path, err := c.saveDistilled("round-trip", "## Summary\nDistilled body.", distilledMeta{
		EntryCount: 12,
		TokensIn:   2000,
		TokensOut:  300,
		PlanBlocks: 2,
		SourceSize: 184320,
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
	assert.Equal(t, int64(184320), loaded.SourceSize, "the staleness fingerprint must survive the essence round-trip")
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

func TestCompactor_DistillChunk_WithMockClient(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a mock client that returns distilled content
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
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
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
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
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
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

	client, err := factory("any-backend", "", 0)
	require.NoError(t, err)

	assert.Same(t, mock, client)
}

// mockBackend implements agent.Backend for testing compactor.
type mockBackend struct {
	history agent.SessionHistory
}

func (m *mockBackend) Name() string    { return "mock-test" }
func (m *mockBackend) Version() string { return "1.0.0" }
func (m *mockBackend) SupportedModes() []agent.ExecutionMode {
	return []agent.ExecutionMode{agent.ModeInteractive, agent.ModeOneshot}
}
func (m *mockBackend) History() agent.SessionHistory                    { return m.history }
func (m *mockBackend) WorkDir() string                                  { return "" }
func (m *mockBackend) SetWorkDir(string)                                {}
func (m *mockBackend) Setup(context.Context, *agent.SetupRequest) error { return nil }
func (m *mockBackend) Execute(context.Context, *agent.ExecuteRequest, io.Writer, io.Writer) (*agent.ExecuteResult, error) {
	return &agent.ExecuteResult{ExitCode: 0}, nil
}
func (m *mockBackend) Cleanup(context.Context) error { return nil }

// mockSessionHistory implements backends.SessionHistory for testing.
type mockSessionHistory struct {
	currentSession *agent.Session
	sessions       map[string]*agent.Session
	sessionList    []agent.SessionMeta
}

func (m *mockSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	if m.currentSession == nil {
		return nil, errors.New("no current session")
	}
	return m.currentSession, nil
}

func (m *mockSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	return m.sessionList, nil
}

func (m *mockSessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	if s, ok := m.sessions[sessionID]; ok {
		return s, nil
	}
	return nil, errors.New("session not found")
}

func (m *mockSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return nil, errors.New("not implemented")
}

func (m *mockSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
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
		currentSession: &agent.Session{
			ID:      "empty-session",
			Entries: []agent.SessionEntry{},
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
		currentSession: &agent.Session{
			ID: "test-compact-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "Hello, how are you?"},
				{Type: agent.EntryTypeAssistant, Content: "I'm doing well, thank you!"},
			},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
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

// TestCompact_MultiChunk_RunsReducePassUnderThreshold pins that the reduce
// (unify) pass runs for any multi-chunk session, even when the combined map
// output is well under ChunkSize. The reduce pass is the only stage that
// normalizes the concatenated per-chunk summaries into the canonical essence
// (YAML frontmatter + "### Open Items"); skipping it leaves raw map output the
// picker can't derive a clean summary or detail lines from.
func TestCompact_MultiChunk_RunsReducePassUnderThreshold(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	// Two large entries split into several chunks at a small ChunkSize, but each
	// chunk distills to a tiny string so the combined output stays under
	// ChunkSize — the exact case the old size gate skipped.
	big := strings.Repeat("the session worked through many decisions and edits. ", 40)
	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "multi-chunk-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: big},
				{Type: agent.EntryTypeAssistant, Content: big},
			},
		},
	}}

	var mu sync.Mutex
	var sawReduce bool
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			mu.Lock()
			if strings.Contains(req.Prompt.Content, sessionDistillReducePrompt) {
				sawReduce = true
			}
			mu.Unlock()
			_, _ = stdout.Write([]byte("x")) // tiny output keeps combined under ChunkSize
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
		ChunkSize:       50, // 200 chars/chunk → the big entries split into many chunks
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)
	require.Greater(t, result.ChunksCreated, 1, "test needs multiple chunks to exercise the reduce pass")

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, sawReduce, "reduce pass must run for multi-chunk sessions even when combined output is under ChunkSize")
}

// TestCompact_DeliversSystemPromptUnderSkipSetup pins the fragment-delivery
// fix: distillation runs with SkipSetup, and the server only hands req.Fragments
// to the backend through Setup — which SkipSetup bypasses. So the distill
// instructions must ride in the prompt itself, or the model never sees them and
// just answers the transcript conversationally (no frontmatter, no Open Items).
func TestCompact_DeliversSystemPromptUnderSkipSetup(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID:      "sysprompt-session",
			Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "hello"}},
		},
	}}

	var sawPrompt string
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			sawPrompt = req.Prompt.Content
			_, _ = stdout.Write([]byte("---\nsummary: ok\n---\n\n### Open Items\n- x"))
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

	assert.Contains(t, sawPrompt, sessionDistillPrompt,
		"the distill system prompt must reach the model in the prompt, since SkipSetup drops req.Fragments")
	assert.Contains(t, sawPrompt, "hello", "the transcript must still be in the prompt")
}

func TestCompact_PreservesPlansVerbatim(t *testing.T) {
	home := testsupport.Isolate(t)
	tmpDir := t.TempDir()
	t.Setenv("CTXLOOM_SESSION_HARP", "plan-harp") // after Isolate, which clears it

	// Seed a plan document in the harp's ctxloom session dir — what the agent
	// would have written during the session. Compaction reads it from there (via
	// the agent server in production; directly here), not from the transcript.
	planBody := "1. design the schema\n2. migrate data with backfill\n3. verify with smoke tests"
	planDir := filepath.Join(home, ".ctxloom", "sessions", "plan-harp")
	require.NoError(t, os.MkdirAll(planDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(planDir, "schema.plan.md"), []byte(planBody), 0o644))

	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "plan-survival",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "make a plan"},
				{Type: agent.EntryTypeAssistant, Content: "plan ready"},
			},
		},
	}}

	// Capture the prompt the LLM sees: plans live in files, so the transcript
	// the LLM summarizes never carries the plan body.
	var sawLLMInput string
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			sawLLMInput = req.Prompt.Content
			_, _ = stdout.Write([]byte("### Summary\nUser asked for a plan."))
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

	assert.NotContains(t, sawLLMInput, planBody, "plan files are not fed to the summary LLM")

	loaded, err := LoadDistilledSession(tmpDir, "plan-survival")
	require.NoError(t, err)
	assert.Equal(t, 1, loaded.PlanBlocks)
	assert.Contains(t, loaded.Body, "## Preserved plans")
	assert.Contains(t, loaded.Body, planBody, "the plan file is re-attached verbatim")
	assert.Contains(t, loaded.Body, "schema", "the plan file's name labels its block")
}

// TestCompact_AllChunksFailed_KeepsPreviousEssence pins the data-loss guard: a
// totally failed distillation (LLM backend down → every chunk a failure
// marker) must abort the save and leave a previously good essence.md and its
// legacy mirror untouched, instead of overwriting them with failure markers.
func TestCompact_AllChunksFailed_KeepsPreviousEssence(t *testing.T) {
	home := testsupport.Isolate(t)
	tmpDir := t.TempDir()

	prior := "---\nsession_id: old\n---\n\n# Session summary\n\nprior good essence\n"
	harpDir := filepath.Join(home, ".ctxloom", "sessions", "fail-harp")
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	essencePath := filepath.Join(harpDir, "essence.md")
	require.NoError(t, os.WriteFile(essencePath, []byte(prior), 0o644))
	legacyPath := filepath.Join(tmpDir, "fail-session.md")
	require.NoError(t, os.WriteFile(legacyPath, []byte(prior), 0o644))

	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "fail-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "hello"},
				{Type: agent.EntryTypeAssistant, Content: "world"},
			},
		},
	}}
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			return 0, errors.New("backend down")
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
		HarpName:        "fail-harp",
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.Error(t, err, "an all-chunks-failed distillation must not report success")
	assert.Contains(t, err.Error(), "all 1 chunks")

	got, err := os.ReadFile(essencePath)
	require.NoError(t, err)
	assert.Equal(t, prior, string(got), "previous essence must survive a total distillation failure")
	gotLegacy, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, prior, string(gotLegacy), "legacy mirror must survive too")
}

// TestCompact_PartialChunkFailure_StillSaves pins the other half of the
// philosophy: partial success is success. One failed chunk among several still
// saves, with the failure marker inline.
func TestCompact_PartialChunkFailure_StillSaves(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	// ~720 chars of transcript so a 100-token (400-char) chunk size yields
	// multiple chunks. The multi-chunk reduce pass is mocked as a pass-through
	// (below) so the failure marker and successful chunks survive into the body
	// unchanged, isolating the partial-failure guarantee.
	content := strings.Repeat("## Section\nalpha beta gamma delta epsilon\n\n", 17)
	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID:      "partial-session",
			Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: content}},
		},
	}}
	// Fail chunk 1 deterministically by its position note, not call order:
	// chunks distill concurrently, so a shared call counter would both race and
	// fail an unpredictable chunk. The reduce (unify) pass runs for any
	// multi-chunk session; mock it as a pass-through so the assertions see the
	// combined map output verbatim.
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			prompt := req.Prompt.Content
			switch {
			case strings.Contains(prompt, sessionDistillReducePrompt):
				_, _ = io.WriteString(stdout, prompt)
			case strings.Contains(prompt, "This is chunk 1 of"):
				return 0, errors.New("flaky")
			default:
				_, _ = stdout.Write([]byte("distilled ok"))
			}
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
		ChunkSize:       100,
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err, "a partially failed distillation must still save")
	require.Greater(t, result.ChunksCreated, 1, "fixture sanity: needs multiple chunks")

	loaded, err := LoadDistilledSession(tmpDir, "partial-session")
	require.NoError(t, err)
	assert.Contains(t, loaded.Body, "Chunk 1 failed", "failed chunk keeps its marker")
	assert.Contains(t, loaded.Body, "distilled ok", "successful chunks are saved")
}

func TestCompact_BySessionID(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	targetSession := &agent.Session{
		ID: "specific-session",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: "Specific request"},
		},
	}
	mockHistory := &mockSessionHistory{
		sessions: map[string]*agent.Session{
			"specific-session": targetSession,
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
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

// =============================================================================
// distillChunks — concurrency: results land in chunk order regardless of which
// goroutine finishes first. Run with -race to also pin the shared-mock safety.
// =============================================================================

func TestCompactor_DistillChunks_PreservesOrderConcurrently(t *testing.T) {
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			// Echo the chunk body so we can assert each output landed in the slot
			// matching its input, even though chunks distill concurrently. The
			// instructions precede the transcript in the prompt, so cut to the
			// <session_log> body.
			_, after, _ := strings.Cut(req.Prompt.Content, "<session_log>\n")
			body := strings.TrimSuffix(after, "\n</session_log>")
			_, _ = stdout.Write([]byte("D:" + body))
			return 0, nil
		},
	}
	c := &Compactor{
		config:        CompactionConfig{LLM: "test-plugin"},
		clientFactory: pb.MockClientFactory(mock),
	}

	chunks := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"}
	out, failed := c.distillChunks(context.Background(), chunks)

	require.Equal(t, 0, failed)
	require.Len(t, out, len(chunks))
	for i, ch := range chunks {
		assert.Equal(t, "D:"+ch, out[i], "chunk %d output landed in the wrong slot", i)
	}
}

// =============================================================================
// finalCompressionPass — uses the dedicated reduce prompt, not the map prompt.
// The reduce prompt re-asserts the mandatory frontmatter and identifier
// preservation the picker depends on.
// =============================================================================

func TestCompactor_FinalCompressionPass_UsesReducePrompt(t *testing.T) {
	var gotPrompt string
	mock := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			gotPrompt = req.Prompt.Content
			_, _ = stdout.Write([]byte("merged essence"))
			return 0, nil
		},
	}
	c := &Compactor{
		config:        CompactionConfig{LLM: "test-plugin"},
		clientFactory: pb.MockClientFactory(mock),
	}

	out := c.finalCompressionPass(context.Background(), "partial one\n---\npartial two")
	assert.Equal(t, "merged essence", out)
	assert.Contains(t, gotPrompt, sessionDistillReducePrompt, "final pass must use the reduce prompt")
	assert.NotContains(t, gotPrompt, sessionDistillPrompt, "final pass must not reuse the map prompt")
}

// =============================================================================
// buildPickerDetail — extra picker lines from the body's Open Items section.
// =============================================================================

func TestBuildPickerDetail(t *testing.T) {
	longBullet := "- " + strings.Repeat("x", 120)
	tests := []struct {
		name     string
		body     string
		expected []string
	}{
		{
			name:     "extracts open items bullets",
			body:     "### Open Items\n- finish the picker\n- write the tests\n\n### State\nin progress",
			expected: []string{"- finish the picker", "- write the tests"},
		},
		{
			name:     "stops at the next section heading",
			body:     "### Open Items\n- only this one\n\n### Decisions\n- not this one",
			expected: []string{"- only this one"},
		},
		{
			name:     "caps at four bullets",
			body:     "### Open Items\n- one\n- two\n- three\n- four\n- five\n- six",
			expected: []string{"- one", "- two", "- three", "- four"},
		},
		{
			name:     "caps each bullet at 80 bytes",
			body:     "### Open Items\n" + longBullet,
			expected: []string{longBullet[:80]},
		},
		{
			name:     "nil when no open items section",
			body:     "### State\n- this is state, not open items",
			expected: nil,
		},
		{
			name:     "ignores prose before the section",
			body:     "Some intro prose.\n\n### Open Items\n- the real item",
			expected: []string{"- the real item"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, buildPickerDetail(tt.body))
		})
	}
}
