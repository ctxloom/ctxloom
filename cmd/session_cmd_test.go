package cmd

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// TestSessionContainsHarpName exercises the harp-name match used by
// `ctxloom session distill <harp>` when no session_id was forward-
// bound. The harp name is guaranteed to appear in the LLM's recorded
// transcript because we inject it via ServerOptions.Instructions on
// MCP initialize. Tests cover the four entry surfaces:
// Content, ToolOutput, ToolName, ToolInput.
func TestSessionContainsHarpName(t *testing.T) {
	harp := "swift-amber-falcon"

	t.Run("matches_Content", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Content: "Your session is named `" + harp + "`. Refer to it…"},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("matches_ToolOutput", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeUser, Content: "hi"},
			{Type: backends.EntryTypeAssistant, Content: "hello"},
			{Type: backends.EntryTypeToolResult, ToolName: "task_list",
				ToolOutput: `[{"harp_id":"swift-amber-falcon","text":"x"}]`},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("matches_ToolInput", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Type: backends.EntryTypeToolUse, ToolName: "TodoWrite",
				ToolInput: json.RawMessage(`{"todos":[{"content":"` + "`" + harp + "`" + ` thing"}]}`)},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("matches_ToolName", func(t *testing.T) {
		// Unusual but possible: a harp-named MCP tool. The matcher
		// shouldn't miss it.
		s := &backends.Session{Entries: []backends.SessionEntry{
			{ToolName: harp},
		}}
		assert.True(t, sessionContainsHarpName(s, harp))
	})

	t.Run("no_match_when_absent", func(t *testing.T) {
		s := &backends.Session{Entries: []backends.SessionEntry{
			{Content: "no harp here"},
			{ToolOutput: "nor here"},
		}}
		assert.False(t, sessionContainsHarpName(s, harp))
	})

	t.Run("empty_session", func(t *testing.T) {
		assert.False(t, sessionContainsHarpName(&backends.Session{}, harp))
	})

	t.Run("nil_session", func(t *testing.T) {
		assert.False(t, sessionContainsHarpName(nil, harp))
	})

	t.Run("empty_harp_never_matches", func(t *testing.T) {
		// An empty harp would substring-match everything. Reject up front.
		s := &backends.Session{Entries: []backends.SessionEntry{{Content: "anything"}}}
		assert.False(t, sessionContainsHarpName(s, ""))
	})
}

// newTestSessionManager opens a sessions.Manager rooted at a temp
// directory so each test gets its own isolated index.yaml.
func newTestSessionManager(t *testing.T) *sessions.Manager {
	t.Helper()
	mgr, err := sessions.Open(filepath.Join(t.TempDir(), "index.yaml"))
	require.NoError(t, err)
	return mgr
}

// TestBindSessionFromPayload covers the SessionStart hook target. The
// happy path is one call with a well-formed payload binding harp →
// session_id. Edge cases enumerated: empty harp, nil manager, missing
// session_id, malformed JSON, harp not in index, harp already bound.
func TestBindSessionFromPayload(t *testing.T) {
	t.Run("happy_path_binds_session_id", func(t *testing.T) {
		mgr := newTestSessionManager(t)
		entry, err := mgr.AssignHarp("/tmp/project", "claude-code")
		require.NoError(t, err)

		payload := `{"session_id":"abc-123","transcript_path":"/t/session.jsonl","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), entry.HarpName, mgr))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "abc-123", got.SessionID)
		assert.Equal(t, "/t/session.jsonl", got.TranscriptPath)
	})

	t.Run("empty_harp_is_noop", func(t *testing.T) {
		mgr := newTestSessionManager(t)
		_, _ = mgr.AssignHarp("/tmp/project", "claude-code")
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), "", mgr)
		assert.NoError(t, err, "no harp means we're not in a ctxloom session — silently succeed")
	})

	t.Run("nil_manager_is_noop", func(t *testing.T) {
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), "harp", nil)
		assert.NoError(t, err)
	})

	t.Run("missing_session_id_is_noop", func(t *testing.T) {
		mgr := newTestSessionManager(t)
		entry, _ := mgr.AssignHarp("/tmp/project", "claude-code")
		require.NoError(t, bindSessionFromPayload(strings.NewReader(`{"transcript_path":"/t/x"}`), entry.HarpName, mgr))
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID, "no session_id in payload → no bind")
	})

	t.Run("malformed_json_is_noop", func(t *testing.T) {
		mgr := newTestSessionManager(t)
		entry, _ := mgr.AssignHarp("/tmp/project", "claude-code")
		require.NoError(t, bindSessionFromPayload(strings.NewReader(`not json`), entry.HarpName, mgr))
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID, "hook must never fail the host backend over a bad message")
	})

	t.Run("unknown_harp_is_noop", func(t *testing.T) {
		mgr := newTestSessionManager(t)
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), "no-such-harp", mgr)
		assert.NoError(t, err, "stale CTXLOOM_SESSION_HARP env shouldn't crash the hook")
	})

	t.Run("already_bound_is_idempotent", func(t *testing.T) {
		mgr := newTestSessionManager(t)
		entry, _ := mgr.AssignHarp("/tmp/project", "claude-code")
		require.NoError(t, mgr.BindSession(entry.HarpName, "first-id", "/orig"))

		// Re-running the hook with a different session_id must NOT
		// overwrite the existing binding — once bound, we trust the
		// first writer.
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"second-id"}`), entry.HarpName, mgr)
		require.NoError(t, err)
		got, _ := mgr.Find(entry.HarpName)
		assert.Equal(t, "first-id", got.SessionID, "first bind wins; second is no-op")
	})
}

// TestBindSessionFromPayload_StdinReaderError exercises the IO failure
// path. errReader always errors on Read; the helper should surface that
// as a wrapped error rather than panicking.
func TestBindSessionFromPayload_StdinReaderError(t *testing.T) {
	mgr := newTestSessionManager(t)
	_, _ = mgr.AssignHarp("/tmp/project", "claude-code")
	err := bindSessionFromPayload(&errReader{}, "anything", mgr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read payload")
}

// errReader satisfies io.Reader and always fails. Used to test stdin-
// failure handling in bindSessionFromPayload.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, assert.AnError
}

// Use json package symbol to suppress unused-import warning in the
// future if we drop a test case that needed json.
var _ = json.Marshal
