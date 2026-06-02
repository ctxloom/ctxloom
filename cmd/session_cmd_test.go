package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/harpmarker"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// TestEmitHarpMarker covers the SessionStart producer side: the bind hook emits
// the harp self-id marker as valid SessionStart hook output so it lands in the
// transcript, and emits nothing when no harp is active.
func TestEmitHarpMarker(t *testing.T) {
	t.Run("emits valid SessionStart marker", func(t *testing.T) {
		var buf bytes.Buffer
		emitHarpMarker(&buf, "plump-loose-sash")

		var out HookOutput
		require.NoError(t, json.Unmarshal(buf.Bytes(), &out))
		require.NotNil(t, out.HookSpecificOutput)
		assert.Equal(t, "SessionStart", out.HookSpecificOutput.HookEventName)
		assert.Equal(t, harpmarker.Format("plump-loose-sash"), out.HookSpecificOutput.AdditionalContext)
		// The decoded additionalContext must be recoverable by the read-time scanner.
		assert.Equal(t, "plump-loose-sash", harpmarker.Scan(buf.Bytes()))
	})

	t.Run("no harp emits nothing", func(t *testing.T) {
		var buf bytes.Buffer
		emitHarpMarker(&buf, "")
		assert.Empty(t, buf.Bytes())
	})
}

// seedHomeSession isolates the home-rooted session index to a temp HOME (which
// operations.BindSession resolves) and seeds a pending harp entry. Tests point
// HOME at a temp dir rather than injecting a manager, since bindSessionFromPayload
// now goes through operations.
func seedHomeSession(t *testing.T) (*sessions.Manager, sessions.Entry) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/tmp/project", "claude-code")
	require.NoError(t, err)
	return mgr, entry
}

// TestBindSessionFromPayload covers the SessionStart hook target. The happy
// path is one call with a well-formed payload binding harp → session_id. Edge
// cases enumerated: empty harp, missing session_id, malformed JSON, harp not in
// index, harp already bound.
func TestBindSessionFromPayload(t *testing.T) {
	t.Run("happy_path_binds_session_id", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		payload := `{"session_id":"abc-123","transcript_path":"/t/session.jsonl","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "abc-123", got.SessionID)
		assert.Equal(t, "/t/session.jsonl", got.TranscriptPath)
	})

	t.Run("empty_harp_is_noop", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), "")
		assert.NoError(t, err, "no harp means we're not in a ctxloom session — silently succeed")
	})

	t.Run("missing_session_id_is_noop", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)
		require.NoError(t, bindSessionFromPayload(strings.NewReader(`{"transcript_path":"/t/x"}`), entry.HarpName))
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID, "no session_id in payload → no bind")
	})

	t.Run("malformed_json_is_noop", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)
		require.NoError(t, bindSessionFromPayload(strings.NewReader(`not json`), entry.HarpName))
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID, "hook must never fail the host backend over a bad message")
	})

	t.Run("unknown_harp_is_noop", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), "no-such-harp")
		assert.NoError(t, err, "stale CTXLOOM_SESSION_HARP env shouldn't crash the hook")
	})

	t.Run("already_bound_is_idempotent", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)
		require.NoError(t, mgr.BindSession(entry.HarpName, "first-id", "/orig"))

		// Re-running the hook with a different session_id must NOT
		// overwrite the existing binding — once bound, we trust the
		// first writer.
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"second-id"}`), entry.HarpName)
		require.NoError(t, err)
		got, _ := mgr.Find(entry.HarpName)
		assert.Equal(t, "first-id", got.SessionID, "first bind wins; second is no-op")
	})
}

// TestBindSessionFromPayload_StdinReaderError exercises the IO failure
// path. errReader always errors on Read; the helper should surface that
// as a wrapped error rather than panicking (before any index access).
func TestBindSessionFromPayload_StdinReaderError(t *testing.T) {
	err := bindSessionFromPayload(&errReader{}, "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read payload")
}

// errReader satisfies io.Reader and always fails. Used to test stdin-
// failure handling in bindSessionFromPayload.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, assert.AnError
}
