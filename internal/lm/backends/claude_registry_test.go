// Tests for the registry-side methods on ClaudeSessionHistory:
// TranscriptPathFromHook, RegisterSession, GetPreviousSession.
// These are 0% covered by the existing claude_session_test.go because
// the registry surface depends on a per-workDir .ctxloom/ephemeral
// directory on disk; the constructor change that propagates h.fs into
// the registry makes hermetic MemMapFs tests possible.
package backends

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// TranscriptPathFromHook
// =============================================================================

func TestClaudeSessionHistory_TranscriptPathFromHook_BuildsExpectedPath(t *testing.T) {
	h := NewClaudeSessionHistory(NewClaudeCode(),
		WithClaudeSessionHomeDir("/synthetic/home"),
	)

	// Claude's per-project transcript dir replaces path separators with
	// dashes: /test/project → -test-project. Pin that here so a future
	// refactor that changes the convention shows up loudly.
	got := h.TranscriptPathFromHook("/test/project", "sess-abc", "ignored")
	want := filepath.Join("/synthetic/home", ".claude", "projects", "-test-project", "sess-abc.jsonl")
	assert.Equal(t, want, got)
}

func TestClaudeSessionHistory_TranscriptPathFromHook_EmptySessionIDReturnsEmpty(t *testing.T) {
	h := NewClaudeSessionHistory(NewClaudeCode(),
		WithClaudeSessionHomeDir("/synthetic/home"),
	)
	assert.Empty(t, h.TranscriptPathFromHook("/p", "", "x"),
		"empty session id is a structural absence, not a guess at the path")
}

// =============================================================================
// RegisterSession + GetPreviousSession
// =============================================================================

func TestClaudeSessionHistory_RegisterSession_PersistsToRegistry(t *testing.T) {
	fs := afero.NewMemMapFs()
	workDir := "/proj"
	require.NoError(t, fs.MkdirAll(filepath.Join(workDir, ".ctxloom", "ephemeral"), 0755))

	h := NewClaudeSessionHistory(NewClaudeCode(), WithClaudeSessionFS(fs))

	require.NoError(t, h.RegisterSession(workDir, 1234, "/transcripts/t1.jsonl"))

	// Re-registering the same path is idempotent (no error, no duplicate).
	require.NoError(t, h.RegisterSession(workDir, 1234, "/transcripts/t1.jsonl"))

	// The registry file should now exist in the MemMapFs.
	exists, err := afero.Exists(fs, filepath.Join(workDir, ".ctxloom", "ephemeral", "claude-session-registry.json"))
	require.NoError(t, err)
	assert.True(t, exists, "registry must persist through the shared fs")
}

func TestClaudeSessionHistory_GetPreviousSession_ReturnsNilWhenSingleSession(t *testing.T) {
	// With only one registered session for the PID, there's no "previous"
	// to return — GetPreviousSession must surface nil, nil rather than
	// the current session itself.
	fs := afero.NewMemMapFs()
	h := NewClaudeSessionHistory(NewClaudeCode(), WithClaudeSessionFS(fs))

	require.NoError(t, h.RegisterSession("/proj", 1234, "/transcripts/only.jsonl"))

	prev, err := h.GetPreviousSession("/proj", 1234)
	require.NoError(t, err)
	assert.Nil(t, prev)
}

func TestClaudeSessionHistory_GetPreviousSession_ReturnsParsedPrior(t *testing.T) {
	fs := afero.NewMemMapFs()
	homeDir := "/home/test"
	workDir := "/proj"

	// First "previous" transcript needs to be readable through h.fs so
	// the registry's parseFunc callback (h.parseSessionFile) can decode it.
	priorPath := filepath.Join("/transcripts", "prior.jsonl")
	priorContent := `{"type":"user","timestamp":"2026-05-27T10:00:00Z","message":{"content":"earlier turn"}}` + "\n"
	require.NoError(t, afero.WriteFile(fs, priorPath, []byte(priorContent), 0644))

	currentPath := filepath.Join("/transcripts", "current.jsonl")
	require.NoError(t, afero.WriteFile(fs, currentPath, []byte(""), 0644))

	h := NewClaudeSessionHistory(NewClaudeCode(),
		WithClaudeSessionFS(fs),
		WithClaudeSessionHomeDir(homeDir),
	)

	// Register prior, then current — GetPreviousSession returns prior.
	require.NoError(t, h.RegisterSession(workDir, 1234, priorPath))
	require.NoError(t, h.RegisterSession(workDir, 1234, currentPath))

	prev, err := h.GetPreviousSession(workDir, 1234)
	require.NoError(t, err)
	require.NotNil(t, prev, "two registered sessions means a previous exists")

	require.Len(t, prev.Entries, 1)
	assert.Equal(t, EntryTypeUser, prev.Entries[0].Type)
	assert.Equal(t, "earlier turn", prev.Entries[0].Content)
}

func TestClaudeSessionHistory_GetPreviousSession_UnknownPIDIsNil(t *testing.T) {
	// No registration ever happened for the PID. Registry returns an
	// empty RegistryData, and GetPreviousSession surfaces nil, nil.
	fs := afero.NewMemMapFs()
	h := NewClaudeSessionHistory(NewClaudeCode(), WithClaudeSessionFS(fs))

	prev, err := h.GetPreviousSession("/proj", 99999)
	require.NoError(t, err)
	assert.Nil(t, prev)
}

func TestClaudeSessionHistory_RegisterSession_FSIsShared(t *testing.T) {
	// This pins the fix in NewClaudeSessionHistory: the registry must
	// share h.fs, not silently fall back to OsFs. If a future refactor
	// breaks that wiring, RegisterSession would either write nothing to
	// the MemMapFs (and we'd see Exists=false) or panic when locking
	// against an OsFs path that doesn't exist.
	fs := afero.NewMemMapFs()
	h := NewClaudeSessionHistory(NewClaudeCode(), WithClaudeSessionFS(fs))

	require.NoError(t, h.RegisterSession("/proj", 42, "/t/path.jsonl"))

	exists, err := afero.Exists(fs, "/proj/.ctxloom/ephemeral/claude-session-registry.json")
	require.NoError(t, err)
	require.True(t, exists, "WithClaudeSessionFS must reach the registry too")

	// Belt-and-suspenders: the registry file is non-empty JSON.
	data, err := afero.ReadFile(fs, "/proj/.ctxloom/ephemeral/claude-session-registry.json")
	require.NoError(t, err)
	assert.Contains(t, string(data), "/t/path.jsonl", "registered path must round-trip")
}
