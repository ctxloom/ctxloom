package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/harpmarker"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestRenderSessionRows_ProjectionShape pins the WS-6 default listing shape
// (CLI-primary reorg plan, decision 13): the table shows HARP, SUMMARY, and
// START — never a LAST ACTIVITY column, which belonged to the old
// Entry-keyed renderSessionTable this replaces. A title-less row still
// renders (as "(no summary)"), and the caller's pre-sorted row order is
// preserved.
func TestRenderSessionRows_ProjectionShape(t *testing.T) {
	testsupport.Isolate(t) // newSessionRow resolves an essence path off HOME; keep it off the real one
	started := time.Date(2026, 7, 17, 17, 27, 32, 0, time.Local)
	rows := []SessionRow{
		newSessionRow(sessions.Entry{HarpName: "swift-amber-falcon", Summary: "Designed the picker", StartedAt: started}, ""),
		newSessionRow(sessions.Entry{HarpName: "plump-loose-sash", StartedAt: started.Add(-time.Hour)}, ""),
	}
	var buf bytes.Buffer
	require.NoError(t, renderSessionRows(&buf, rows))
	out := buf.String()

	assert.Contains(t, out, "HARP", "header must name the harp column")
	assert.Contains(t, out, "SUMMARY", "header must name the summary column")
	assert.Contains(t, out, "START", "header must name the start column")
	assert.NotContains(t, out, "LAST ACTIVITY", "the old last-activity column is gone")
	assert.Contains(t, out, "2026-07-17 17:27:32", "start is rendered to second granularity")
	assert.Contains(t, out, "(no summary)", "a title-less row still renders")
	assert.Less(t, strings.Index(out, "swift-amber-falcon"), strings.Index(out, "plump-loose-sash"),
		"caller's pre-sorted order is preserved")
}

// TestRenderSessionRows_EmptyShowsPlaceholder pins the empty-listing UX
// renderSessionTable used to own: no rows renders a friendly "(no sessions)"
// line rather than an empty/absent table.
func TestRenderSessionRows_EmptyShowsPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderSessionRows(&buf, nil))
	assert.Contains(t, buf.String(), "(no sessions)")
}

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
	testsupport.Isolate(t)
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

	t.Run("antigravity_payload_binds_conversation_id", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		// agy fires session-bind as a PreToolUse hook (pre_tool_fallback);
		// the payload shape mirrors a verbatim live capture.
		payload := `{"conversationId":"c6a3e887-aeea","stepIdx":3,` +
			`"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi","Cwd":"/tmp/project"}},` +
			`"transcriptPath":"/h/.gemini/antigravity-cli/brain/c6a3e887-aeea/.system_generated/logs/transcript_full.jsonl",` +
			`"workspacePaths":["/tmp/project"]}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "c6a3e887-aeea", got.SessionID)
		assert.Contains(t, got.TranscriptPath, "transcript_full.jsonl")
	})

	t.Run("antigravity_payload_skips_harp_marker", func(t *testing.T) {
		// On agy the hook runs as PreToolUse, where stdout is reserved for
		// decision JSON — the marker envelope must not be emitted.
		assert.True(t, isAntigravityHookPayload([]byte(`{"conversationId":"x","toolCall":{"name":"run_command","args":{}}}`)))
		assert.False(t, isAntigravityHookPayload([]byte(`{"session_id":"x","hook_event_name":"SessionStart"}`)))
		assert.False(t, isAntigravityHookPayload([]byte(`not json`)))
	})

	t.Run("kiro_agentSpawn_payload_falls_back_to_KIRO_SESSION_ID_env", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		// Live-verified against real kiro-cli 2.12.1 (2026-07-21, dizzy-zoom):
		// kiro's agentSpawn hook stdin payload carries NO session identifier
		// at all — {"hook_event_name":"agentSpawn","cwd":"...","prompt":"..."}
		// — but kiro-cli sets KIRO_SESSION_ID in the hook subprocess's OWN
		// environment, and that value was confirmed (by direct sqlite query
		// against conversations_v2) to equal the conversation's real
		// conversation_id. bindSessionFromPayload must fall back to it when
		// the payload itself carries none.
		t.Setenv("KIRO_SESSION_ID", "7150ba7d-fe97-46a2-b68f-d228359ef546")
		payload := `{"hook_event_name":"agentSpawn","cwd":"/tmp/project","prompt":"say hi"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "7150ba7d-fe97-46a2-b68f-d228359ef546", got.SessionID,
			"kiro's own KIRO_SESSION_ID env var must bind when the JSON payload carries no session id")
	})

	t.Run("KIRO_SESSION_ID_env_never_overrides_an_explicit_payload_session_id", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		t.Setenv("KIRO_SESSION_ID", "should-not-be-used")
		payload := `{"session_id":"explicit-id","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "explicit-id", got.SessionID, "an explicit payload session id always wins over the env fallback")
	})

	t.Run("empty_harp_is_noop", func(t *testing.T) {
		testsupport.Isolate(t)
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
		// U042-F03: a malformed SessionStart hook payload silently skipped the
		// harp->session_id bind with NOTHING reported anywhere — not even the
		// caller's own warning, since bindSessionFromPayload returned nil (no
		// error to warn about). This is one of the two live-reproducible
		// causes of "no canonical transcript captured": a harp can go its
		// entire life with SessionID never bound, and an operator has no way
		// to learn why. bindSessionFromPayload must never FAIL the host's
		// tool call over this (still asserted below via require.NoError), but
		// it must warn.
		var buf bytes.Buffer
		restore := clidiag.SetSink(&buf)
		defer restore()

		require.NoError(t, bindSessionFromPayload(strings.NewReader(`not json`), entry.HarpName))
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID, "hook must never fail the host backend over a bad message")
		assert.Contains(t, buf.String(), entry.HarpName,
			"a malformed hook payload must warn (naming the harp), not vanish with zero diagnostic")
	})

	t.Run("unknown_harp_is_noop", func(t *testing.T) {
		testsupport.Isolate(t)
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

// U042-F04: distillMissingOrStale's per-entry chdir used to only ever go
// FORWARD (into e.ProjectDir when non-empty) and never restore origWd for an
// entry with no ProjectDir of its own — so an entry with an empty ProjectDir
// silently ran config.Load() from whatever directory the PREVIOUS entry in
// the loop happened to chdir into. situateForEntry is the extracted,
// independently-testable cwd-management step that fixes this: it restores
// origWd when the entry has no ProjectDir, rather than leaving the process
// wherever the last chdir left it.
func TestSituateForEntry(t *testing.T) {
	testsupport.Isolate(t)
	origWd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(origWd) }()

	dirA := t.TempDir()
	dirB := t.TempDir()

	t.Run("chdirs into a non-empty ProjectDir", func(t *testing.T) {
		require.NoError(t, os.Chdir(origWd))
		require.NoError(t, situateForEntry(&sessions.Entry{ProjectDir: dirA}, origWd))
		cwd, err := os.Getwd()
		require.NoError(t, err)
		assert.Equal(t, realpath(t, dirA), realpath(t, cwd))
	})

	t.Run("an empty ProjectDir restores origWd, not wherever a prior entry left the process", func(t *testing.T) {
		// Simulate the sequence a real loop produces: a previous entry with
		// ProjectDir=dirB left the process there.
		require.NoError(t, os.Chdir(dirB))
		require.NoError(t, situateForEntry(&sessions.Entry{ProjectDir: ""}, origWd))
		cwd, err := os.Getwd()
		require.NoError(t, err)
		assert.Equal(t, realpath(t, origWd), realpath(t, cwd),
			"an entry with no ProjectDir of its own must run from origWd, not a sibling entry's leftover cwd")
	})
}

// realpath resolves symlinks (macOS/some CI temp dirs are themselves
// symlinks, e.g. /tmp -> /private/tmp) so a cwd comparison isn't defeated by
// two spellings of the same directory.
func realpath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return resolved
}
