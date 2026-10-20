package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// mockTurn is one line of the mock engine's vendor log.
type mockTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func writeMockVendorLog(t *testing.T, path string, turns ...mockTurn) {
	t.Helper()
	var b strings.Builder
	for _, turn := range turns {
		line, err := json.Marshal(turn)
		require.NoError(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

// seedMockSession indexes a mock-engine session in projectDir bound, in
// order, to one vendor log per segment: every binding after the first
// rotates the one before it into the session's lineage, as a /clear does. It
// returns the harp and the vendor logs' paths.
func seedMockSession(t *testing.T, projectDir string, segments ...[]mockTurn) (*sessions.Manager, string, []string) {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(projectDir, string(mock.Name))
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, string(mock.Name))
	dir := t.TempDir()
	logs := make([]string, 0, len(segments))
	for i, turns := range segments {
		path := filepath.Join(dir, "vendor-"+string(rune('a'+i))+".jsonl")
		writeMockVendorLog(t, path, turns...)
		require.NoError(t, mgr.BindSession(entry.HarpName, "sess-"+string(rune('a'+i)), path))
		logs = append(logs, path)
	}
	return mgr, entry.HarpName, logs
}

var twoTurns = []mockTurn{
	{"user", "first question"}, {"assistant", "first answer"},
	{"user", "second question"}, {"assistant", "second answer"},
}

// showLines decodes `transcript show --format json` into its NDJSON lines.
func showLines(t *testing.T, out string) []watchEventLine {
	t.Helper()
	require.True(t, strings.HasSuffix(out, "\n"), "NDJSON ends on a newline: %q", out)
	var lines []watchEventLine
	for _, raw := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var line watchEventLine
		require.NoError(t, json.Unmarshal([]byte(raw), &line), "every stdout line is one JSON event: %q", raw)
		lines = append(lines, line)
	}
	return lines
}

// showShape is the NDJSON reduced to what a reader filters on: an entry as
// "type: content", a boundary as its index range.
func showShape(lines []watchEventLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		switch {
		case l.Entry != nil:
			out = append(out, l.Entry.Type+": "+l.Entry.Content)
		case l.Boundary != nil:
			b, _ := json.Marshal(l.Boundary)
			out = append(out, "boundary "+string(b))
		default:
			out = append(out, "other")
		}
	}
	return out
}

// Text mode prints the whole transcript as watch renders it, a rule closing
// each turn, and nothing after it.
func TestSessionTranscriptShow_PrintsTheWholeTranscriptAsText(t *testing.T) {
	_, harp, _ := seedMockSession(t, testsupport.ProjectDir(t), twoTurns)

	out, err := execRootCmd(t, "session", "transcript", "show", harp, "--format", "text")
	require.NoError(t, err)
	assert.Equal(t, "user: first question\nassistant: first answer\n"+watchBoundaryRule+"\n"+
		"user: second question\nassistant: second answer\n"+watchBoundaryRule+"\n", out)
}

// --format json is watch's NDJSON: an entry line per turn, a boundary line
// closing each turn over its entries.
func TestSessionTranscriptShow_JSONIsWatchsNDJSON(t *testing.T) {
	_, harp, _ := seedMockSession(t, testsupport.ProjectDir(t), twoTurns)

	out, err := execRootCmd(t, "session", "transcript", "show", harp, "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"user: first question", "assistant: first answer", `boundary {"toIndex":2}`,
		"user: second question", "assistant: second answer", `boundary {"fromIndex":2,"toIndex":4}`,
	}, showShape(showLines(t, out)))
}

// Off a terminal, with no --format, the dump is machine-readable: what a pipe
// into jq reads.
func TestSessionTranscriptShow_PipedWithNoFormatIsNDJSON(t *testing.T) {
	_, harp, _ := seedMockSession(t, testsupport.ProjectDir(t), twoTurns)

	out, err := execRootCmd(t, "session", "transcript", "show", harp)
	require.NoError(t, err)
	assert.Len(t, showLines(t, out), 6)
}

// A session rotated by a /clear reads as one transcript, its segments in the
// order the session lived them.
func TestSessionTranscriptShow_StitchesRotationSegmentsInOrder(t *testing.T) {
	_, harp, _ := seedMockSession(t, testsupport.ProjectDir(t),
		[]mockTurn{{"user", "before the clear"}, {"assistant", "old answer"}},
		[]mockTurn{{"user", "after the clear"}, {"assistant", "new answer"}},
	)

	out, err := execRootCmd(t, "session", "transcript", "show", harp, "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, []string{
		"user: before the clear", "assistant: old answer", `boundary {"toIndex":2}`,
		"user: after the clear", "assistant: new answer", `boundary {"fromIndex":2,"toIndex":4}`,
	}, showShape(showLines(t, out)))
}

// The dump reads the session as its engine's log has it now, not as its last
// capture left it.
func TestSessionTranscriptShow_ReadsTheVendorLogGrownSinceTheLastCapture(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp := seedGrownClaudeSession(t, dir, 6)

	out, err := execRootCmd(t, "session", "transcript", "show", harp, "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, grownTurn)
}

// A refresh that fails warns on stderr, and the dump goes on with the copy
// the session has; stdout carries the transcript and nothing else.
func TestSessionTranscriptShow_FailedRefreshWarnsAndDumpsTheCopy(t *testing.T) {
	mgr, harp, logs := seedMockSession(t, testsupport.ProjectDir(t), twoTurns[:2])
	healVendorTranscriptOnExit(harp)
	writeMockVendorLog(t, logs[0], twoTurns...)
	// A version no mock adapter carries: the refresh refuses.
	require.NoError(t, mgr.RecordEngineVersion(harp, "0.0.1"))

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	out, stderr, err := execRootCmdBoth(t, "session", "transcript", "show", harp, "--format", "text")
	require.NoError(t, err, "a failed refresh must not block the dump")
	assert.Equal(t, "user: first question\nassistant: first answer\n"+watchBoundaryRule+"\n", out,
		"stdout is the copy the session had, and nothing else")
	assert.Contains(t, warnings.String()+stderr, operations.ReadRefreshFailed)
}

// An unknown session is refused the way `session show` refuses it.
func TestSessionTranscriptShow_UnknownSessionIsRefused(t *testing.T) {
	testsupport.ProjectDir(t)

	out, err := execRootCmd(t, "session", "transcript", "show", "nosuch-session-name")
	require.Error(t, err)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok, "the miss names its fix: %v", err)
	assert.Equal(t, sessionListFix, fix)
	assert.Empty(t, out)
}

// A session with nothing captured and nothing to capture from is refused, not
// dumped as an empty transcript.
func TestSessionTranscriptShow_NeverCapturedSessionIsRefused(t *testing.T) {
	_, harp := seedEndedSession(t, testsupport.ProjectDir(t), string(mock.Name))

	out, err := execRootCmd(t, "session", "transcript", "show", harp)
	require.ErrorIs(t, err, errNoCapturedTranscript)
	assert.Empty(t, out)
}

// A --format outside text and json is refused before the session is read,
// naming the value the caller gave: a session with nothing captured is
// refused for the format, not for its transcript.
func TestSessionTranscriptShow_RefusesAFormatItCannotRender(t *testing.T) {
	_, harp := seedEndedSession(t, testsupport.ProjectDir(t), string(mock.Name))

	for _, format := range []string{"yaml", "bogus"} {
		out, err := execRootCmd(t, "session", "transcript", "show", harp, "--format", format)
		require.Error(t, err, format)
		assert.NotErrorIs(t, err, errNoCapturedTranscript, format)
		assert.Contains(t, err.Error(), format)
		assert.Empty(t, out, format)
	}
}

// A session record that cannot be read is an error, not an unknown session.
func TestSessionTranscriptShow_UnreadableSessionRecordIsAnError(t *testing.T) {
	_, harp, _ := seedMockSession(t, testsupport.ProjectDir(t), twoTurns)
	sidecar, err := paths.HarpSidecarPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sidecar, []byte("{not json"), 0o644))

	out, err := execRootCmd(t, "session", "transcript", "show", harp)
	require.Error(t, err)
	_, remedied := clifmt.RemedyOf(err)
	assert.False(t, remedied, "an unreadable record is not a miss: %v", err)
	assert.Empty(t, out)
}

// A captured transcript nothing in which decodes is an error, not an empty
// dump.
func TestSessionTranscriptShow_UnreadableTranscriptIsAnError(t *testing.T) {
	_, harp := seedEndedSession(t, testsupport.ProjectDir(t), string(mock.Name))
	canon, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canon), 0o755))
	require.NoError(t, os.WriteFile(canon, []byte("not a transcript\n"), 0o644))

	out, err := execRootCmd(t, "session", "transcript", "show", harp)
	require.Error(t, err)
	assert.NotErrorIs(t, err, errNoCapturedTranscript)
	assert.Empty(t, out)
}
