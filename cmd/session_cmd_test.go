package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
)

// TestHarpSessionMarker pins the canonical discovery-marker format. The
// emitter (cmd/mcp_server.go) and the raw-transcript scanner
// (discoverSessionByHarpName) both derive the search string from this one
// function, so they can't drift; this test guards the wire format itself.
// The harp name is quoted so the marker is self-delimiting (see the
// prefix-collision case in TestFileContainsMarker).
func TestHarpSessionMarker(t *testing.T) {
	assert.Equal(t, `ctxloom harp session: "swift-amber-falcon"`, harpSessionMarker("swift-amber-falcon"))
}

// instructionsEntry mimics the raw jsonl entry Claude Code writes for an MCP
// server's initialize instructions block — the one place ctxloom's harp
// marker legitimately lands. The marker rides in the body alongside the rest
// of the instructions; the normalized parser drops this entry, but the
// scoped byte scan in fileContainsMarker finds it. Marshalled (not
// hand-built) so the marker's embedded quotes are escaped exactly as the
// real transcript escapes them.
func instructionsEntry(t *testing.T, body string) string {
	return mustJSONLine(t, map[string]any{
		"type": "attachment",
		"attachment": map[string]any{
			"type":        instructionsAttachmentType,
			"addedBlocks": []string{"...\n\n" + body + "\n..."},
		},
	})
}

// mentionEntry mimics a conversational entry (user turn, tool result, picker
// output) that merely contains the marker text — the false-positive source
// the instructions-entry scoping exists to reject.
func mentionEntry(t *testing.T, body string) string {
	return mustJSONLine(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": body},
	})
}

func mustJSONLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// TestFileContainsMarker covers the scoped transcript scan that
// discoverSessionByHarpName uses: the marker matches only when it rides in
// the MCP-instructions entry, a conversational mention of the same marker
// does not, a marker in some other attachment subtype does not, an instructions
// entry for a different (or prefix-sharing) harp does not, a missing file is a
// non-match (not an error), and an empty marker never matches.
func TestFileContainsMarker(t *testing.T) {
	harp := "swift-amber-falcon"
	marker := harpSessionMarker(harp)

	t.Run("present_in_instructions_entry", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(instructionsEntry(t, marker)), 0o600))
		assert.True(t, fileContainsMarker(path, marker))
	})

	t.Run("conversational_mention_does_not_match", func(t *testing.T) {
		// The core of the tightening: a later session that discusses this
		// harp (distilling it, picker/list_sessions output, code quoting the
		// marker format) carries the exact marker bytes in a user/tool entry.
		// It must NOT bind, or distill would latch onto the wrong session.
		path := filepath.Join(t.TempDir(), "session.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(mentionEntry(t, marker)), 0o600))
		assert.False(t, fileContainsMarker(path, marker))
	})

	t.Run("mention_before_real_instructions_entry_still_matches", func(t *testing.T) {
		// A harp session can also mention its own marker later on; the scan
		// must not stop at the first (mention) line and miss the real entry.
		path := filepath.Join(t.TempDir(), "session.jsonl")
		lines := mentionEntry(t, marker) + "\n" + instructionsEntry(t, marker) + "\n"
		require.NoError(t, os.WriteFile(path, []byte(lines), 0o600))
		assert.True(t, fileContainsMarker(path, marker))
	})

	t.Run("marker_in_other_attachment_subtype_does_not_match", func(t *testing.T) {
		// Only the instructions subtype counts. A file-read or other
		// attachment that happens to carry the marker text is a mention.
		path := filepath.Join(t.TempDir(), "session.jsonl")
		line := mustJSONLine(t, map[string]any{
			"type":       "attachment",
			"attachment": map[string]any{"type": "file", "content": marker},
		})
		require.NoError(t, os.WriteFile(path, []byte(line), 0o600))
		assert.False(t, fileContainsMarker(path, marker))
	})

	t.Run("absent_when_only_other_harp_present", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(instructionsEntry(t, harpSessionMarker("other-harp-name"))), 0o600))
		assert.False(t, fileContainsMarker(path, marker))
	})

	t.Run("prefix_sharing_harp_does_not_match", func(t *testing.T) {
		// Quoting the name makes the marker self-delimiting: the marker for
		// `swift-amber` must not match a transcript carrying the marker for
		// `swift-amber-falcon` (an unquoted prefix would have).
		path := filepath.Join(t.TempDir(), "session.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(instructionsEntry(t, harpSessionMarker("swift-amber-falcon"))), 0o600))
		assert.False(t, fileContainsMarker(path, harpSessionMarker("swift-amber")))
	})

	t.Run("missing_file_is_non_match", func(t *testing.T) {
		assert.False(t, fileContainsMarker(filepath.Join(t.TempDir(), "nope.jsonl"), marker))
	})

	t.Run("empty_marker_never_matches", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "session.jsonl")
		require.NoError(t, os.WriteFile(path, []byte(instructionsEntry(t, "anything")), 0o600))
		assert.False(t, fileContainsMarker(path, ""))
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
