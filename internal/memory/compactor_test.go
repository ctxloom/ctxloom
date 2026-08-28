package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestCompactor_SessionToText(t *testing.T) {
	c := &Compactor{config: CompactionConfig{}}

	session := &agent.Session{
		ID: "test-session",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: "Hello"},
			{Type: agent.EntryTypeAssistant, Content: "Hi there!"},
			{Type: agent.EntryTypeToolUse, ToolName: "Read", ToolInput: []byte(`{"path":"/test"}`)},
			{Type: agent.EntryTypeToolResult, ToolName: "Read", ToolOutput: "REDERIVABLE_FILE_CONTENTS"},
			{Type: agent.EntryTypeToolUse, ToolName: "Write", ToolInput: []byte(`{"path":"/w"}`)},
			{Type: agent.EntryTypeToolResult, ToolName: "Write", ToolOutput: "AUTHORITATIVE_WRITE_RESULT"},
			{Type: agent.EntryTypeSystem, Content: "System message"},
		},
	}

	text, _ := c.sessionToText(session)

	assert.Contains(t, text, "## User\nHello")
	assert.Contains(t, text, "## Assistant\nHi there!")
	assert.Contains(t, text, "## System: System message")

	// Both CALLS survive: the essence must still record what was examined.
	assert.Contains(t, text, "## Tool Call: Read")
	assert.Contains(t, text, "## Tool Call: Write")

	// Neither RESULT keeps its body. Every non-error result reduces to its
	// shape, whatever tool produced it -- a uniform truncation cost roughly a
	// quarter of the rendered transcript to deliver severed fragments.
	//
	// Asserting the absence of the bodies alone would be satisfied by a
	// renderer that never ran, so the shape lines are asserted alongside them:
	// both headers present, both payloads gone, and a shape carrying real
	// counts rather than an empty marker.
	assert.NotContains(t, text, "REDERIVABLE_FILE_CONTENTS")
	assert.NotContains(t, text, "AUTHORITATIVE_WRITE_RESULT")
	assert.Contains(t, text, "## Tool Result: Read")
	assert.Contains(t, text, "## Tool Result: Write")
	assert.Equal(t, 2, strings.Count(text, "bytes, 1 lines]"),
		"both results must render a shape line with real counts")
}

// TestCompactor_SessionToText_ThinkingExcludedByDefault is the
// payload assertion: a thinking entry's content must not reach the text
// handed to distillation unless IncludeThinking is explicitly set. "It ran"
// proves nothing here — assert the actual bytes.
func TestCompactor_SessionToText_ThinkingExcludedByDefault(t *testing.T) {
	session := &agent.Session{
		ID: "thinking-session",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: "ASK"},
			{Type: agent.EntryTypeThinking, Content: "SCRATCH_REASONING_TEXT"},
			{Type: agent.EntryTypeAssistant, Content: "CONCLUSION"},
		},
	}

	suppressed, _ := (&Compactor{config: CompactionConfig{}}).sessionToText(session)
	assert.Contains(t, suppressed, "ASK")
	assert.Contains(t, suppressed, "CONCLUSION")
	assert.NotContains(t, suppressed, "SCRATCH_REASONING_TEXT", "thinking content must not reach distillation by default")

	included, _ := (&Compactor{config: CompactionConfig{IncludeThinking: true}}).sessionToText(session)
	assert.Contains(t, included, "SCRATCH_REASONING_TEXT", "IncludeThinking:true must preserve the escape hatch")
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

	text, _ := c.sessionToText(session)

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

	text, _ := c.sessionToText(session)

	assert.Contains(t, text, "[ERROR]")
}

func TestDistilledSession_RoundTrip(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()
	const harp = "round-trip-harp"

	c := &Compactor{config: CompactionConfig{OutputDir: tmpDir}}
	path, err := c.saveDistilled("round-trip", "## Summary\nDistilled body.", distilledMeta{
		HarpName:   harp,
		EntryCount: 12,
		TokensIn:   2000,
		TokensOut:  300,
		PlanBlocks: 2,
		SourceSize: 184320,
	})
	require.NoError(t, err)

	// saveDistilled returns the harp's CURRENT essence, which is what a caller
	// prints and what the picker reads; the rotation copy read back below is
	// the other half of the same write.
	essencePath, perr := paths.HarpEssencePath(harp)
	require.NoError(t, perr)
	assert.Equal(t, essencePath, path)

	loaded, err := LoadDistilledSession(tmpDir, "round-trip")
	require.NoError(t, err)
	assert.Equal(t, "round-trip", loaded.SessionID)
	assert.Equal(t, 300, loaded.TokensOut)
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

func TestCompactor_RunDistill_WithMockClient(t *testing.T) {
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
			HarpName:  "compactor-under-test",
		},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	result, err := c.runDistill(context.Background(), sessionDistillPrompt, "Original session content")
	require.NoError(t, err)

	assert.Equal(t, "Distilled: key decisions and outcomes", result)
	assert.Equal(t, 1, mockClient.RunCalls)
}

func TestCompactor_RunDistill_ClientError(t *testing.T) {
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

	_, err := c.runDistill(context.Background(), sessionDistillPrompt, "content")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection failed")
}

func TestCompactor_RunDistill_NonZeroExit(t *testing.T) {
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

	_, err := c.runDistill(context.Background(), sessionDistillPrompt, "content")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 1")
}

// An LLM that exits 0 having written nothing is a FAILURE, not an
// empty distillation. Treated as success it produced an empty body which
// saveDistilled then atomically wrote over a previously good essence.md.
func TestCompactor_RunDistill_EmptyOutputIsAFailure(t *testing.T) {
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			return 0, nil // exit 0, not one byte of output
		},
	}

	c := &Compactor{
		config:        CompactionConfig{LLM: "test-plugin"},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	_, err := c.runDistill(context.Background(), sessionDistillPrompt, "content")
	require.Error(t, err, "exit 0 with empty stdout must not read as a successful distillation")
	assert.Contains(t, err.Error(), "no output")
}

// The other half of the same rule: even if an empty body reaches saveDistilled by some
// other route, it must never replace an existing essence. Distillation exists
// to preserve context; silently zeroing it is the worst possible outcome.
func TestCompactor_SaveDistilled_RefusesEmptyBody(t *testing.T) {
	c := &Compactor{config: CompactionConfig{OutputDir: t.TempDir()}}

	_, err := c.saveDistilled("some-session", "   \n\n  ", distilledMeta{})
	require.Error(t, err, "an empty distilled body must not be written over a good essence")
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
	testsupport.Isolate(t) // see TestCompact_EmptySession: isolates CTXLOOM_SESSION_HARP
	// so identityBoundSessionID can't resolve a real ambient session.
	mockHistory := &mockSessionHistory{currentSession: nil}
	mockBe := &mockBackend{history: mockHistory}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	assert.Error(t, err)
}

// TestCompact_EmptySession is the empty-session regression test: a session with
// zero main-thread entries must short-circuit straight to a dumped, trivial
// essence — succeeding, not erroring — and must never reach the distillation
// LLM pipeline. The ClientFactory below fails the test outright if the
// compactor ever tries to spawn an LLM plugin, so a regression that routes an
// empty session back through the distillation call is
// caught even if the result's shape still happens to look plausible.
func TestCompact_EmptySession(t *testing.T) {
	testsupport.Isolate(t) // isolates CTXLOOM_SESSION_HARP too — this test's mock
	// has no harp-index binding, and without isolation an ambient real session's
	// CTXLOOM_SESSION_HARP would make identityBoundSessionID resolve a REAL
	// session id from the real ~/.ctxloom/sessions/index.yaml.
	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID:      "empty-session",
			Entries: []agent.SessionEntry{},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			t.Fatal("compaction pipeline invoked for an empty session; must short-circuit to a dump before any LLM call")
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err, "an empty session must dump successfully, not error")
	assert.NotEmpty(t, result.DistilledPath)

	data, err := os.ReadFile(result.DistilledPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), emptySessionPlaceholder, "the dumped essence must carry the trivial placeholder body")
}

// TestCompact_SidechainEntriesExcluded: the reader now surfaces
// subagent-interior (sidechain) entries for viewers, but distillation keeps
// its historic main-thread-only input — sidechain content never reaches the
// distilling LLM, and an all-sidechain session is "no entries".
func TestCompact_SidechainEntriesExcluded(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "sidechain-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "MAIN_THREAD_ASK"},
				{Type: agent.EntryTypeAssistant, Content: "SIDECHAIN_INTERIOR", Sidechain: true},
				{Type: agent.EntryTypeAssistant, Content: "MAIN_THREAD_ANSWER"},
			},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	var mu sync.Mutex
	var prompts []string
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			mu.Lock()
			prompts = append(prompts, req.GetPrompt().GetContent())
			mu.Unlock()
			_, _ = stdout.Write([]byte("Distilled."))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, prompts)
	joined := strings.Join(prompts, "\n")
	assert.Contains(t, joined, "MAIN_THREAD_ASK")
	assert.Contains(t, joined, "MAIN_THREAD_ANSWER")
	assert.NotContains(t, joined, "SIDECHAIN_INTERIOR", "sidechain content must not reach distillation")
}

// TestCompact_ThinkingExcludedFromLLMPrompt is the end-to-end
// payload assertion: the thinking-budget slice (2026-07-16) means a real
// interactive session's canonical transcript now carries EntryTypeThinking
// entries, and this asserts they never reach the prompt the compression LLM
// actually sees, all the way through the real Compact() pipeline.
func TestCompact_ThinkingExcludedFromLLMPrompt(t *testing.T) {
	testsupport.Isolate(t)

	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "thinking-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "MAIN_THREAD_ASK"},
				{Type: agent.EntryTypeThinking, Content: "SCRATCH_REASONING_TEXT"},
				{Type: agent.EntryTypeAssistant, Content: "MAIN_THREAD_ANSWER"},
			},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	var mu sync.Mutex
	var prompts []string
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			mu.Lock()
			prompts = append(prompts, req.GetPrompt().GetContent())
			mu.Unlock()
			_, _ = stdout.Write([]byte("Distilled."))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, prompts)
	joined := strings.Join(prompts, "\n")
	assert.Contains(t, joined, "MAIN_THREAD_ASK")
	assert.Contains(t, joined, "MAIN_THREAD_ANSWER")
	assert.NotContains(t, joined, "SCRATCH_REASONING_TEXT", "thinking content must not reach the compression LLM's prompt")
}

// TestCompact_AllSidechainSessionIsEmpty: a session whose every entry is
// subagent-interior filters down to zero main-thread entries, so it must take
// the same dump short-circuit as a literally-empty session (see
// TestCompact_EmptySession) — succeeding with a trivial essence, never
// reaching the LLM.
func TestCompact_AllSidechainSessionIsEmpty(t *testing.T) {
	testsupport.Isolate(t) // see TestCompact_EmptySession: isolates CTXLOOM_SESSION_HARP
	// so identityBoundSessionID can't resolve a real ambient session.
	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "interior-only",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeAssistant, Content: "interior", Sidechain: true},
			},
		},
	}
	mockBe := &mockBackend{history: mockHistory}

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			t.Fatal("compaction pipeline invoked for an all-sidechain (empty main-thread) session")
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.NoError(t, err, "an all-sidechain session must dump successfully, not error")
}

// An empty session must NOT replace an already-distilled essence
// with the 54-byte placeholder. Re-distillation is triggered automatically by
// the staleness path, and loadSessionToCompact falls back to the current
// session when a bound transcript is gone — so a good essence could be wiped
// by a routine, automatic re-distill of a session that reads as empty.
func TestCompact_EmptySessionDoesNotOverwriteExistingEssence(t *testing.T) {
	testsupport.Isolate(t)
	outDir := t.TempDir()

	const sessionID = "previously-distilled"
	const goodEssence = "---\nsession_id: previously-distilled\n---\n\n# Session summary\n\nReal, hard-won distilled context.\n"
	existing := filepath.Join(outDir, sessionID+".md")
	require.NoError(t, os.WriteFile(existing, []byte(goodEssence), 0o644))

	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{ID: sessionID, Entries: []agent.SessionEntry{}},
	}
	mockBe := &mockBackend{history: mockHistory}
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			t.Fatal("empty session must not reach the LLM")
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       outDir,
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.NoError(t, err, "an empty session is still not a failure")

	after, readErr := os.ReadFile(existing)
	require.NoError(t, readErr)
	assert.Equal(t, goodEssence, string(after),
		"the placeholder must not have replaced a real distilled essence")
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
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "test-compact-session", result.SessionID)
	assert.NotEmpty(t, result.DistilledPath)
	assert.Greater(t, result.TotalTokensIn, 0)
	assert.Greater(t, result.TotalTokensOut, 0)

	// Verify file was created
	_, err = os.Stat(result.DistilledPath)
	require.NoError(t, err)
}

// TestCompact_EnforcesMaxEssenceChars pins the requirement: even a
// "successful" distillation pipeline (every LLM call exits 0) must never save
// or return an essence body over the named MaxEssenceChars ceiling. Here the
// reduce call itself exits 0 but its OWN output is oversized — a distinct
// failure mode from "the reduce call errored" (covered by
// TestCompact_ReduceFailure_NeverFallsBackToUnboundedRawCombined below): the
// pipeline behaved, the model just didn't compress enough.
func TestCompact_EnforcesMaxEssenceChars(t *testing.T) {
	testsupport.Isolate(t)
	tmpDir := t.TempDir()

	big := strings.Repeat("the session worked through many decisions and edits. ", 200)
	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: "oversized-reduce-session",
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: big},
				{Type: agent.EntryTypeAssistant, Content: big},
			},
		},
	}}

	oversized := strings.Repeat("z", MaxEssenceChars+1)
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			// The distillation "succeeds" (exit 0) but its output is over the
			// bound -- a model that ignored its character budget.
			_, _ = stdout.Write([]byte(oversized))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       tmpDir,
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.Error(t, err, "an essence over the MaxEssenceChars bound must fail loud, not save")
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), fmt.Sprintf("%d", MaxEssenceChars),
		"the error must name the ceiling constant, not a magic number")

	// Nothing must have been written for this session — a refusal must never
	// leave the oversized body on disk either.
	entries, rerr := os.ReadDir(tmpDir)
	require.NoError(t, rerr)
	for _, e := range entries {
		data, rerr := os.ReadFile(filepath.Join(tmpDir, e.Name()))
		require.NoError(t, rerr)
		assert.NotContains(t, string(data), oversized[:1000], "refused essence must not have been written to disk")
	}
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
		HarpName:        "compactor-under-test",
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
	assert.Contains(t, loaded.Body, "## Preserved plans")
	assert.Contains(t, loaded.Body, planBody, "the plan file is re-attached verbatim")
	assert.Contains(t, loaded.Body, "schema", "the plan file's name labels its block")
}

// TestCompact_DistillationFailed_KeepsPreviousEssence pins the data-loss guard: a
// totally failed distillation (LLM backend down → every chunk a failure
// marker) must abort the save and leave a previously good essence.md and its
// legacy mirror untouched, instead of overwriting them with failure markers.
func TestCompact_DistillationFailed_KeepsPreviousEssence(t *testing.T) {
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
	require.Error(t, err, "a failed distillation must not report success")
	// The error is the ONLY diagnosis most callers ever get: warnf is dropped
	// whenever Progress is nil, which is every MCP relay call. A cause-free
	// failure sent a real investigation to the wrong subsystem entirely.
	assert.Contains(t, err.Error(), "backend down", "the failure must carry the underlying cause")

	got, err := os.ReadFile(essencePath)
	require.NoError(t, err)
	assert.Equal(t, prior, string(got), "previous essence must survive a total distillation failure")
	gotLegacy, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, prior, string(gotLegacy), "legacy mirror must survive too")
}

// refusingSessionSource is a pb.SessionSource whose every method fails the
// test if called — used to prove PreloadedSession short-circuits
// loadSessionToCompact entirely, without consulting source/CurrentSession.
type refusingSessionSource struct{ t *testing.T }

func (r refusingSessionSource) GetSession(context.Context, string) (*agent.Session, error) {
	r.t.Fatal("GetSession must not be called when PreloadedSession is set")
	return nil, nil
}
func (r refusingSessionSource) ListSessions(context.Context) ([]agent.SessionMeta, error) {
	r.t.Fatal("ListSessions must not be called when PreloadedSession is set")
	return nil, nil
}
func (r refusingSessionSource) CurrentSession(context.Context) (*agent.Session, error) {
	r.t.Fatal("CurrentSession must not be called when PreloadedSession is set")
	return nil, nil
}

// TestCompactor_LoadSessionToCompact_PreloadedSessionBypassesSource pins the
// container-harp distill fix: when only the mounted transcript
// path is known host-side (no bound session_id), the caller loads the
// session by path itself and hands it to the compactor via
// CompactionConfig.PreloadedSession. loadSessionToCompact must return it
// directly, never touching c.source (identity-bound lookup, CurrentSession,
// or otherwise) — the source is wired to fail the test if consulted.
func TestCompactor_LoadSessionToCompact_PreloadedSessionBypassesSource(t *testing.T) {
	preloaded := &agent.Session{
		ID: "preloaded-session",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: "hi from container harp"},
		},
	}
	c := &Compactor{
		config: CompactionConfig{PreloadedSession: preloaded},
		source: refusingSessionSource{t: t},
	}

	got, err := c.loadSessionToCompact(context.Background())
	require.NoError(t, err)
	assert.Same(t, preloaded, got, "loadSessionToCompact must return the preloaded session as-is")
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
		HarpName:        "compactor-under-test",
		SessionID:       "specific-session",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "specific-session", result.SessionID)
}

// TestCompact_CurrentSession_PrefersIdentityBoundOverMtime is the
// regression test: when compacting "the current session" (no SessionID given),
// the compactor must use the harp's session-index binding — recorded once at
// session-start and never touched again — rather than whatever the backend's
// mtime-position "current session" pick returns. A backend rewrites/touches a
// transcript's mtime when a session is resumed, so the mtime-newest transcript
// is not reliably the one that just ended; here it deliberately reports a
// DIFFERENT, "newer" session than the one actually bound to the harp.
func TestCompact_CurrentSession_PrefersIdentityBoundOverMtime(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/project", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "correct-session", ""))

	mockHistory := &mockSessionHistory{
		// What an mtime-position pick ("current session") would wrongly return —
		// a stale/resumed transcript that out-ranks the real one by mtime.
		currentSession: &agent.Session{
			ID:      "stale-resumed-session",
			Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "stale"}},
		},
		sessions: map[string]*agent.Session{
			"correct-session": {
				ID:      "correct-session",
				Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "correct"}},
			},
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
		OutputDir:       t.TempDir(),
		Backend:         "claude-code",
		HarpName:        entry.HarpName,
		// SessionID intentionally left empty: "compact my current session".
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "correct-session", result.SessionID,
		"must use the harp's identity-bound session, not the mtime-position pick")
}

// TestCompact_CurrentSession_FallsBackToMtimeWhenNoHarp pins the genuine-last-
// resort behavior: with no harp (nothing to bind identity against — e.g. a
// bare `ctxloom memory compact` run outside any tracked session), the mtime-
// based CurrentSession path still resolves as before.
func TestCompact_CurrentSession_FallsBackToMtimeWhenNoHarp(t *testing.T) {
	testsupport.Isolate(t)

	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID:      "mtime-current-session",
			Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "hi"}},
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
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
		// No HarpName, no SessionID.
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "mtime-current-session", result.SessionID)
}

// TestCompact_IdentityBoundStaleFallsBackToCurrentSession is a
// regression test: the harp is bound to a session id in the index, but that
// id's transcript no longer exists in the backend's store (rotated/deleted —
// a stale index entry). loadSessionToCompact must degrade to the
// mtime-based CurrentSession — the genuine last resort — rather than hard-
// erroring, the same fault-tolerant posture the empty-SessionID path always
// had before the identity-bound lookup was added.
func TestCompact_IdentityBoundStaleFallsBackToCurrentSession(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/project", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(entry.HarpName, "dead-session", ""))

	mockHistory := &mockSessionHistory{
		// "dead-session" is intentionally absent from sessions: its transcript
		// is gone. currentSession is the genuine last-resort fallback.
		currentSession: &agent.Session{
			ID:      "mtime-current-session",
			Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "hi"}},
		},
		sessions: map[string]*agent.Session{},
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
		OutputDir:       t.TempDir(),
		Backend:         "claude-code",
		HarpName:        entry.HarpName,
		// SessionID intentionally left empty: "compact my current session".
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err, "a stale identity binding must degrade to CurrentSession, not hard-error")
	assert.Equal(t, "mtime-current-session", result.SessionID)
}

// TestCompact_ExplicitSessionIDStaleHardErrors pins the boundary the fix must
// NOT cross: `session distill <harp>` (and any other explicit-SessionID
// caller) asked for exactly that session, so a missing transcript must still
// hard-error rather than silently substituting CurrentSession.
func TestCompact_ExplicitSessionIDStaleHardErrors(t *testing.T) {
	testsupport.Isolate(t)

	mockHistory := &mockSessionHistory{
		currentSession: &agent.Session{
			ID:      "mtime-current-session",
			Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: "hi"}},
		},
		sessions: map[string]*agent.Session{},
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
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
		Backend:         "claude-code",
		SessionID:       "dead-session", // explicit, and absent from sessions
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	assert.Error(t, err, "an explicitly requested session that can't be found must still hard-error")
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

// TestNewCompactor_UnopenableSessionIndex_ReportsTheRealReason pins the error a
// user actually sees when the canonical session index cannot be opened on a
// retired-scraper backend. Those backends (claude-code, the default, among
// them) have no legacy scraper leg, so the canonical layer is the only
// transcript source; when its index fails to open there is nothing to read.
// That used to surface as "backend %q does not support session history" — a
// true statement about a different problem, pointing at a remedy (switch
// backends) that cannot fix an unwritable index.
func TestNewCompactor_UnopenableSessionIndex_ReportsTheRealReason(t *testing.T) {
	home := testsupport.Isolate(t)

	// Make sessions.Open("") fail: it MkdirAll's the index's parent, so a plain
	// file where that directory belongs is enough.
	sessionsPath := filepath.Join(home, ".ctxloom", "sessions")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionsPath), 0o755))
	require.NoError(t, os.WriteFile(sessionsPath, []byte("not a directory"), 0o644))

	// The fixture must be hostile from the code-under-test's vantage point
	// before anything is asserted about behaviour: a temp HOME that the
	// compactor does not actually consult would make this test green for the
	// wrong reason.
	_, openErr := sessions.Open("")
	require.Error(t, openErr, "fixture is not hostile: sessions.Open still succeeds")

	backend := "claude-code"
	require.True(t, pb.IsRetiredScraperBackend(backend),
		"fixture assumes a backend with no legacy scraper leg")

	c, err := NewCompactor(CompactionConfig{Backend: backend})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.Compact(ctx)
	require.Error(t, err)
	require.NotErrorIs(t, err, context.DeadlineExceeded)

	assert.Contains(t, err.Error(), "session index",
		"the failure that actually happened must be the one reported")
	assert.NotContains(t, err.Error(), "does not support session history",
		"reporting an unsupported backend sends the user after the wrong remedy")
}

// TestCompact_EntriesThatRenderToNothing_ShortCircuit covers the state
// isEmptySession's entry count cannot see: entries are present, but the text
// handed to distillation is empty. A session whose only main-thread entries
// are `thinking` reaches exactly that, because appendEntryText suppresses
// thinking by policy unless IncludeThinking is set.
//
// Without the render check the pipeline chunks the empty string into one
// chunk and spawns an LLM subprocess to summarize a transcript containing
// nothing, then saves whatever comes back over the session's essence. The
// ClientFactory fails the test if any LLM call is attempted.
func TestCompact_EntriesThatRenderToNothing_ShortCircuit(t *testing.T) {
	testsupport.Isolate(t)

	thinkingOnly := []agent.SessionEntry{
		{Type: agent.EntryTypeThinking, Content: "let me consider the options"},
		{Type: agent.EntryTypeThinking, Content: "still considering"},
	}

	// Fixture hostility check: these entries must be non-empty AND must render
	// to nothing. If either half stops holding, this test is no longer about
	// the defect it names.
	require.NotEmpty(t, thinkingOnly)
	probe, _ := (&Compactor{}).sessionToText(&agent.Session{Entries: thinkingOnly})
	require.Empty(t, strings.TrimSpace(probe),
		"fixture is not hostile: these entries render to non-empty text")

	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{ID: "thinking-only-session", Entries: thinkingOnly},
	}}
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			t.Fatalf("an LLM subprocess was spawned to distil an empty transcript; prompt was %q", req.Prompt.Content)
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err, "nothing to distil is not a failure")
	require.NotEmpty(t, result.DistilledPath)
	data, err := os.ReadFile(result.DistilledPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), emptySessionPlaceholder)

	// The same session WITH thinking included renders real text, so it must go
	// down the ordinary pipeline — the short-circuit keys on the rendered
	// output, not on the entry types.
	var spawned int
	includeClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			spawned++
			_, _ = io.WriteString(stdout, "distilled ok")
			return 0, nil
		},
	}
	inclusive, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(includeClient),
		OutputDir:       t.TempDir(),
		HarpName:        "compactor-under-test",
		IncludeThinking: true,
	})
	require.NoError(t, err)
	_, err = inclusive.Compact(context.Background())
	require.NoError(t, err)
	assert.Positive(t, spawned, "with thinking included the transcript is not empty and must be distilled")
}

// A distillation with no harp has nowhere to go, and must say so rather than
// invent a location.
//
// This used to resolve a default under the CURRENT WORKING DIRECTORY, which is
// the shape the refusal replaces: every CLI distill path chdirs into the
// session's own project dir and back out again, so a cwd-derived default named
// a different file depending on when it was resolved, and its unconditional
// MkdirAll minted a stray .ctxloom under whatever directory the process
// happened to be in — which config's app-dir walk would later adopt as a
// project. An essence belongs to a harp; without one there is no answer, and
// guessing produced exactly that stray directory.
//
// Asserts the EFFECT, not just the error: nothing may be written anywhere under
// the working directory.
func TestSaveDistilled_NoHarpRefusesRatherThanGuessingALocation(t *testing.T) {
	testsupport.ProjectDir(t)
	wd, err := os.Getwd()
	require.NoError(t, err)

	c := &Compactor{config: CompactionConfig{}}
	require.Empty(t, c.config.OutputDir, "this pin exercises the no-OutputDir path")

	path, err := c.saveDistilled("anchored", "## Summary\nbody.", distilledMeta{})
	require.Error(t, err, "a distillation with no harp must refuse, not resolve some default")
	assert.Empty(t, path, "a refused save must not name a file it did not write")
	assert.Contains(t, err.Error(), "harp", "the error must name what is missing")

	strayDir := filepath.Join(wd, ".ctxloom", "sessions")
	_, statErr := os.Stat(strayDir)
	assert.True(t, os.IsNotExist(statErr),
		"the refusal must not mint %s on the way out — that stray directory is what config's app-dir walk later adopts as a project", strayDir)

	found, ok := c.existingEssence("anchored", "")
	assert.False(t, ok, "nothing was written, so nothing may be found")
	assert.Empty(t, found)
}

// An unreadable session index must not be indistinguishable from "this harp has
// no entry". The bind is first-bind-wins and is never retried, so a harp that
// misses it here has no session id for the rest of its life and every later
// `session distill`/resume fails with "no session bound" — with nothing on the
// record saying why. The lookup failure is a degradation and the compactor
// already has a sink for degradations.
func TestUpdateSessionIndex_WarnsWhenTheIndexCannotBeRead(t *testing.T) {
	home := testsupport.Isolate(t)
	indexPath := filepath.Join(home, ".ctxloom", "sessions", "index.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(indexPath), 0o755))
	require.NoError(t, os.WriteFile(indexPath, []byte("sessions: [not: a list of entries\n"), 0o644))

	// The bind arm is reached only through Find, so assert the fixture is
	// hostile from updateSessionIndex's own vantage point before asserting
	// anything about what it reports.
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	_, ferr := mgr.Find("lively-index-harp")
	require.Error(t, ferr, "the fixture index must be unreadable, or this pin proves nothing")

	var sink bytes.Buffer
	c := &Compactor{config: CompactionConfig{Progress: &sink}}
	// An empty summary skips the SetSummary arm, so the lookup failure is the
	// only thing that can put anything in the sink.
	c.updateSessionIndex("lively-index-harp", "sess-1", "", nil, 0)

	assert.Contains(t, sink.String(), "lively-index-harp",
		"an unreadable index must be reported, not silently taken for an absent entry")
}

// Distillation is headless: there is no human to answer an engine that stops to
// ask, so the request must leave here in ONESHOT with a posture that cannot
// block. The gRPC server floors a ONESHOT whose posture would block, so this is
// the SECOND altitude of that invariant and the public behaviour is identical
// either way — the point is that the request the compactor SENDS says so. The
// sibling one-shot call in the trigger-triage path spells this differently and
// relies on the floor alone; anything that unifies the two must not quietly
// take this with it.
func TestRunDistill_SendsAHeadlessSafeOneShotRequest(t *testing.T) {
	testsupport.Isolate(t)

	var sawOpts *pb.RunOptions
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			sawOpts = req.Options
			_, _ = stdout.Write([]byte("distilled"))
			return 0, nil
		},
	}

	c := &Compactor{
		config:        CompactionConfig{LLM: "mock", Model: "haiku"},
		clientFactory: pb.MockClientFactory(mockClient),
	}
	out, err := c.runDistill(context.Background(), "instructions", "transcript")
	require.NoError(t, err)
	require.Equal(t, "distilled", out)

	require.NotNil(t, sawOpts, "the pin is worthless unless the request actually reached the client")
	assert.Equal(t, pb.ExecutionMode_ONESHOT, sawOpts.Mode)
	assert.True(t, sawOpts.SkipSetup, "distillation must stay in minimal mode")
	mode, ok := agent.ParsePermissionMode(sawOpts.PermissionMode)
	require.True(t, ok, "the request must name a parseable permission posture, got %q", sawOpts.PermissionMode)
	assert.True(t, mode.SafeHeadless(),
		"a headless distillation must not send a posture that would stop to ask, got %q", mode)
}

// TestRunDistill_ForwardsConfiguredEnvOntoTheRequest pins the channel by which
// a distillation subprocess receives the credentials its config declares.
//
// runDistill built its RunOptions with PermissionMode, Mode, Model and
// SkipSetup and NO Env at all, while every other RunStart-issuing caller
// forwards the resolved label's env (internal/cli/run.go's llmEnvFor ->
// st.runEnv). Since llm.configs.<label>.env is the documented home for a
// backend's API key, a distiller whose key lived there ran unconfigured — and
// an unconfigured backend does not error, it just behaves as though nothing
// was set. SkipSetup makes this the ONLY channel: it bypasses Setup, which is
// what would otherwise deliver configuration.
//
// Asserting the request's Env rather than any observable downstream effect is
// deliberate: the effect of a missing credential is a backend quietly doing
// the wrong thing, which is exactly what this codebase keeps failing to notice.
func TestRunDistill_ForwardsConfiguredEnvOntoTheRequest(t *testing.T) {
	var gotEnv map[string]string
	var sawRequest bool

	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			sawRequest = true
			gotEnv = req.GetOptions().GetEnv()
			_, _ = stdout.Write([]byte("distilled"))
			return 0, nil
		},
	}

	c := &Compactor{
		config: CompactionConfig{
			LLM:       "test-plugin",
			OutputDir: t.TempDir(),
			HarpName:  "compactor-under-test",
			Env:       map[string]string{"ANTHROPIC_API_KEY": "sk-from-config", "OTHER": "keep"},
		},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	_, err := c.runDistill(context.Background(), sessionDistillPrompt, "session content")
	require.NoError(t, err)

	require.True(t, sawRequest, "the distiller was never invoked, so this proves nothing about what it received")
	assert.Equal(t, "sk-from-config", gotEnv["ANTHROPIC_API_KEY"],
		"a credential declared in the label's config env must reach the distillation subprocess")
	assert.Equal(t, "keep", gotEnv["OTHER"],
		"the whole configured env is forwarded, not a hand-picked subset")
}

// TestCompact_ResultSessionIDIsTheKeyTheEssenceWasWrittenUnder pins the
// contract every caller that reads an essence back depends on: the key
// CompactionResult reports must be a key saveDistilled actually wrote.
//
// The two can diverge, and did. Compact resolves its own session (result.
// SessionID = session.ID) and keys saveDistilled off that same value, so for an
// interactive session whose vendor id is a UUID the essence lands under the
// HARP. cli.distillSessionOnce used to read back with the id its CALLER passed
// instead, which nothing writes — so a completely successful distillation
// reported "couldn't read it back", and the cache lookup on the way in missed
// forever, re-distilling an essence already on disk.
//
// Fixing the caller is not enough on its own: it now trusts this invariant, so
// the invariant needs a test of its own. A mutation keying saveDistilled off
// anything other than the value Compact reports kills this immediately.
func TestCompact_ResultSessionIDIsTheKeyTheEssenceWasWrittenUnder(t *testing.T) {
	testsupport.Isolate(t)
	outputDir := t.TempDir()

	// The shape that broke: the session's own id is NOT a plausible vendor
	// UUID, it is the harp, because that is what Compact resolves to.
	const resolvedID = "shut-hoary-yahoo"
	mockBe := &mockBackend{history: &mockSessionHistory{
		currentSession: &agent.Session{
			ID: resolvedID,
			Entries: []agent.SessionEntry{
				{Type: agent.EntryTypeUser, Content: "where did the essence go"},
				{Type: agent.EntryTypeAssistant, Content: "written under one key, read under another"},
			},
		},
	}}
	const body = "Distilled: the write key and the read key must agree."
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			_, _ = stdout.Write([]byte(body))
			return 0, nil
		},
	}

	compactor, err := NewCompactor(CompactionConfig{
		BackendOverride: mockBe,
		ClientFactory:   pb.MockClientFactory(mockClient),
		OutputDir:       outputDir,
		HarpName:        "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, result.SessionID, "a distillation that wrote a file must report the key it used")

	// The assertion that matters: reading back by the REPORTED key finds the
	// essence, and finds the real body rather than an empty file.
	loaded, err := LoadDistilledSession(outputDir, result.SessionID)
	require.NoError(t, err, "LoadDistilledSession(outputDir, result.SessionID) must find what Compact just wrote")
	assert.Contains(t, loaded.Body, body, "the essence read back must carry the distilled content, not be empty")
}

// runDistill's contract with Distill: the transcript travels enveloped as a
// <session_log>, after the system prompt. The envelope is what tells the model
// which part of the prompt is material rather than instruction; losing it
// leaves the transcript indistinguishable from the instructions above it.
func TestCompactor_RunDistill_EnvelopesContentAsSessionLog(t *testing.T) {
	var captured *pb.RunStart
	mockClient := &pb.MockClient{
		RunFunc: func(ctx context.Context, req *pb.RunStart, stdout, stderr io.Writer) (int32, error) {
			captured = req
			_, _ = stdout.Write([]byte("distilled"))
			return 0, nil
		},
	}
	c := &Compactor{
		config:        CompactionConfig{LLM: "test-plugin"},
		clientFactory: pb.MockClientFactory(mockClient),
	}

	_, err := c.runDistill(context.Background(), "SYSTEM PROMPT", "the transcript")
	require.NoError(t, err)
	require.NotNil(t, captured)
	require.NotNil(t, captured.Prompt)
	assert.Contains(t, captured.Prompt.Content, "<session_log>\nthe transcript\n</session_log>")
	assert.Less(t,
		strings.Index(captured.Prompt.Content, "SYSTEM PROMPT"),
		strings.Index(captured.Prompt.Content, "<session_log>"),
		"the instruction must precede the material")
}
