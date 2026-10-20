package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/harpmarker"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestRenderSessionRows_ProjectionShape pins the default listing shape
// (CLI-primary reorg plan): the table shows HARP, SUMMARY, and
// START — never a LAST ACTIVITY column, which belonged to the old
// Entry-keyed renderSessionTable this replaces. A title-less row still
// renders (as "(no summary)"), and the caller's pre-sorted row order is
// preserved.
func TestRenderSessionRows_ProjectionShape(t *testing.T) {
	testsupport.Isolate(t) // keep the listing off the real HOME
	started := time.Date(2026, 7, 17, 17, 27, 32, 0, time.Local)
	rows := []SessionRow{
		newSessionRow(viewTitled(sessions.Entry{HarpName: "swift-amber-falcon", StartedAt: started}, "Designed the picker")),
		newSessionRow(operations.ViewSession(sessions.Entry{HarpName: "plump-loose-sash", StartedAt: started.Add(-time.Hour)})),
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
		emitHarpMarker(&buf, claudeCodec(t), "plump-loose-sash")

		out := decodeClaudeAnswer(t, buf.Bytes())
		require.NotNil(t, out.HookSpecificOutput)
		assert.Equal(t, "SessionStart", out.HookSpecificOutput.HookEventName)
		assert.Equal(t, harpmarker.Format("plump-loose-sash"), out.HookSpecificOutput.AdditionalContext)
		// The decoded additionalContext must be recoverable by the read-time scanner.
		assert.Equal(t, "plump-loose-sash", harpmarker.Scan(buf.Bytes()))
	})

	t.Run("no harp emits nothing", func(t *testing.T) {
		var buf bytes.Buffer
		emitHarpMarker(&buf, claudeCodec(t), "")
		assert.Empty(t, buf.Bytes())
	})
}

// TestEmitHarpMarker_FailureIsReported is the SILENTNOOP half: the marker is
// the ONLY index-independent way a transcript says which harp owns it, so a
// run that emits zero marker bytes and says nothing leaves an operator with an
// unattributable transcript and no trace of why. stdout stays the hook's
// contract channel in every case — the report goes to the diagnostic channel.
func TestEmitHarpMarker_FailureIsReported(t *testing.T) {
	t.Run("no active harp is reported", func(t *testing.T) {
		var diag bytes.Buffer
		restore := clidiag.SetSink(&diag)
		t.Cleanup(restore)

		var buf bytes.Buffer
		emitHarpMarker(&buf, claudeCodec(t), "")

		assert.Empty(t, buf.Bytes(), "stdout carries hook output only — never a diagnostic")
		assert.Contains(t, diag.String(), "CTXLOOM_SESSION_HARP",
			"a session that gets no harp self-id marker must say so, and name the input that was missing")
	})

	t.Run("a failed marker write is reported", func(t *testing.T) {
		var diag bytes.Buffer
		restore := clidiag.SetSink(&diag)
		t.Cleanup(restore)

		emitHarpMarker(&failingWriter{err: errors.New("stdout broke")}, claudeCodec(t), "plump-loose-sash")

		assert.Contains(t, diag.String(), "stdout broke",
			"a marker that could not be written must be reported, not swallowed into an exit-0 no-op")
	})
}

// seedHomeSession isolates the home-rooted session index to a temp HOME (which
// operations.BindSession resolves) and seeds a pending harp entry. Tests point
// HOME at a temp dir rather than injecting a manager, since bindSessionFromPayload
// now goes through operations.
func seedHomeSession(t *testing.T) (*sessions.Manager, sessions.Entry) {
	t.Helper()
	testsupport.Isolate(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.AssignHarp("/tmp/project", "claude-code")
	require.NoError(t, err)
	_, err = mgr.RecordOutputDir(entry.HarpName, t.TempDir())
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
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), claudeCodec(t), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "abc-123", got.SessionID)
		assert.Equal(t, "/t/session.jsonl", got.TranscriptPath)
	})

	t.Run("an_explicit_payload_session_id_binds", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		payload := `{"session_id":"explicit-id","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), claudeCodec(t), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "explicit-id", got.SessionID, "the session id the payload carries is what binds")
	})

	t.Run("empty_harp_is_noop", func(t *testing.T) {
		testsupport.Isolate(t)
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), claudeCodec(t), "")
		assert.NoError(t, err, "no harp means we're not in a ctxloom session — silently succeed")
	})

	t.Run("missing_session_id_is_noop", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)
		require.NoError(t, bindSessionFromPayload(strings.NewReader(`{"transcript_path":"/t/x"}`), claudeCodec(t), entry.HarpName))
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID, "no session_id in payload → no bind")
	})

	t.Run("malformed_json_is_an_error", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)
		// A malformed session_start payload used to skip the harp->session
		// bind with only a warning, and the hook exited 0: a harp could go
		// its entire life with SessionID never bound, and nothing failed.
		// The bind not happening is now an ERROR naming the harp, which the
		// process exits non-zero on.
		//
		// MUTATION -- return nil when the codec's Decode fails -- turns this
		// red.
		err := bindSessionFromPayload(strings.NewReader(`not json`), claudeCodec(t), entry.HarpName)
		require.Error(t, err)
		assert.Contains(t, err.Error(), entry.HarpName)
		got, _ := mgr.Find(entry.HarpName)
		assert.Empty(t, got.SessionID)
	})

	t.Run("unknown_harp_is_noop", func(t *testing.T) {
		testsupport.Isolate(t)
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"x"}`), claudeCodec(t), "no-such-harp")
		assert.NoError(t, err, "stale CTXLOOM_SESSION_HARP env shouldn't crash the hook")
	})

	t.Run("already_bound_and_payload_names_no_transcript_is_noop", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)
		require.NoError(t, mgr.BindSession(entry.HarpName, "first-id", "/orig"))

		// A payload carrying an id but NO transcript_path cannot establish
		// that the engine rotated, so it must not displace a live binding.
		err := bindSessionFromPayload(strings.NewReader(`{"session_id":"second-id"}`), claudeCodec(t), entry.HarpName)
		require.NoError(t, err)
		got, _ := mgr.Find(entry.HarpName)
		assert.Equal(t, "first-id", got.SessionID, "an id-only payload never re-points")
		assert.Equal(t, "/orig", got.TranscriptPath, "and never moves the transcript path")
	})

	// The /clear rotation, end to end through the hook target. claude-code
	// starts a NEW transcript UUID under the same live process and fires
	// SessionStart again; the harp's binding must follow, or every reader that
	// trusts it (resume, transcript watch, the exit-time capture) goes on
	// reading a transcript the session abandoned.
	t.Run("rotated_transcript_repoints_the_binding", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		first := `{"session_id":"pre-clear-uuid","transcript_path":"/t/pre-clear.jsonl","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(first), claudeCodec(t), entry.HarpName))

		second := `{"session_id":"post-clear-uuid","transcript_path":"/t/post-clear.jsonl","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(second), claudeCodec(t), entry.HarpName))

		got, err := mgr.Find(entry.HarpName)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "post-clear-uuid", got.SessionID,
			"the harp must name the transcript the session is actually growing")
		assert.Equal(t, "/t/post-clear.jsonl", got.TranscriptPath,
			"a binding left on the pre-clear file makes every downstream read confidently wrong")
	})

	t.Run("repeat_hook_firing_with_the_same_transcript_is_idempotent", func(t *testing.T) {
		mgr, entry := seedHomeSession(t)

		payload := `{"session_id":"same-uuid","transcript_path":"/t/same.jsonl","hook_event_name":"SessionStart"}`
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), claudeCodec(t), entry.HarpName))
		require.NoError(t, bindSessionFromPayload(strings.NewReader(payload), claudeCodec(t), entry.HarpName))

		got, _ := mgr.Find(entry.HarpName)
		assert.Equal(t, "same-uuid", got.SessionID)
		assert.Equal(t, "/t/same.jsonl", got.TranscriptPath)
	})
}

// TestBindSessionFromPayload_StdinReaderError exercises the IO failure
// path. errReader always errors on Read; the helper should surface that
// as a wrapped error rather than panicking (before any index access).
func TestBindSessionFromPayload_StdinReaderError(t *testing.T) {
	err := bindSessionFromPayload(&errReader{}, claudeCodec(t), "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read payload")
}

// errReader satisfies io.Reader and always fails. Used to test stdin-
// failure handling in bindSessionFromPayload.
type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, assert.AnError
}

// --- Phase 2: direct tests for the extracted `session` RunE bodies ----------

// An empty session index renders `[]` under `session list --format json`, on
// BOTH the per-project and the --all path: `null` is
// the difference between "no sessions" and "the field is missing" for a
// consumer iterating it with jq.
func TestSessionList_EmptyIndexRendersEmptyJSONList(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	t.Setenv("HOME", dir)

	for _, all := range []bool{false, true} {
		entries, err := loadSessionEntries(all)
		require.NoError(t, err, "all=%v", all)
		cmd, out := formatCmd("json")
		require.NoError(t, emitSessionRows(cmd, operations.ViewSessions(entries, nil)))
		assert.Equal(t, "[]\n", out.String(), "all=%v", all)
	}
}

// `session show` is the session's record, in every format: its title (the
// first user prompt), state, engine and times; a session whose transcript
// holds no prompt yet shows the placeholder and exits 0.
func TestSessionShow_ShowsTheRecordInEveryFormat(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	titled := recordPrompts(t, dir, "Wire the keep mark into the sweep\nmore")
	_, untitled := seedEndedSession(t, dir, "claude-code")

	out, err := execRootCmd(t, "session", "show", titled, "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "Wire the keep mark into the sweep")
	assert.Contains(t, out, "engine:")

	out, err = execRootCmd(t, "session", "show", titled, "--format", "json")
	require.NoError(t, err)
	var got operations.SessionView
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, titled, got.Harp)
	assert.Equal(t, "Wire the keep mark into the sweep", got.Title)
	assert.NotContains(t, out, "essence")
	assert.NotContains(t, out, "compacted")

	out, err = execRootCmd(t, "session", "show", untitled, "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "(no summary)")
}

// A session that ended before its engine launched says why in `session show`,
// in text and in JSON, and in the `session list` row: the terminal output that
// said it is gone, and the session is otherwise a directory and a timestamp.
func TestSessionShow_PrintsTheEndReason(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	require.NoError(t, mgr.RecordEndReason(harp, sessions.EndReason{
		Exit:     3,
		Class:    sessions.EndClassFatalStartup,
		Message:  "ctxloom: aborting startup: 1 fatal finding(s)",
		Findings: []string{"  - [bundle] failed to load bundle \"x\"\n    fix: repair it"},
	}))

	out, err := execRootCmd(t, "session", "show", harp, "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "ended before its engine launched: fatal-startup (exit 3)")
	assert.Contains(t, out, "ctxloom: aborting startup: 1 fatal finding(s)")
	assert.Contains(t, out, "failed to load bundle \"x\"")
	assert.Contains(t, out, "fix: repair it")

	out, err = execRootCmd(t, "session", "show", harp, "--format", "json")
	require.NoError(t, err)
	var got operations.SessionView
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	require.NotNil(t, got.EndReason)
	assert.Equal(t, 3, got.EndReason.Exit)
	assert.Equal(t, sessions.EndClassFatalStartup, got.EndReason.Class)
	assert.Len(t, got.EndReason.Findings, 1)

	row := newSessionRow(operations.ViewSession(sessions.Entry{HarpName: harp, EndReason: got.EndReason}))
	assert.Contains(t, row.Summary, "ended before launch: fatal-startup (exit 3)")
}

// A session whose engine launched has no end reason to show.
func TestSessionShow_NoEndReasonPrintsNothingAboutOne(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")

	out, err := execRootCmd(t, "session", "show", harp, "--format", "text")
	require.NoError(t, err)
	assert.NotContains(t, out, "ended before its engine launched")
	out, err = execRootCmd(t, "session", "show", harp, "--format", "json")
	require.NoError(t, err)
	assert.NotContains(t, out, "end_reason")
}
