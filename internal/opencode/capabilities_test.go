package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fixture bytes captured from a real opencode 1.18.1: `session list --format
// json` and `export <id>`. See capabilities.go for why the reader drives those
// commands rather than the private on-disk store.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

// runnerReturning builds a fake opencode runner: `session list ...` yields
// listBytes, `export <id>` yields exportBytes (or exportErr). dir is ignored
// here (most tests don't care about it); TestExecCLI_PassesDirToTestSeam below
// asserts it is actually threaded through for the one test that does.
func runnerReturning(listBytes, exportBytes []byte, exportErr error) func(dir string, args ...string) ([]byte, error) {
	return func(dir string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "export" {
			if exportErr != nil {
				return nil, exportErr
			}
			return exportBytes, nil
		}
		return listBytes, nil
	}
}

// TestExecCLI_PassesDirToTestSeam pins a real bug: the test seam used to
// discard `dir`, the one argument execCLI's own doc comment calls
// load-bearing ("run elsewhere it silently lists a different project"),
// which left the must-run-in-workDir invariant structurally untestable. Now
// the seam receives dir and this asserts it matches what ListSessions was
// called with.
func TestExecCLI_PassesDirToTestSeam(t *testing.T) {
	var gotDir string
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(func(dir string, args ...string) ([]byte, error) {
		gotDir = dir
		return []byte("[]"), nil
	}))
	_, err := h.ListSessions("/proj/workdir")
	require.NoError(t, err)
	assert.Equal(t, "/proj/workdir", gotDir, "execCLI must thread dir through to the injected test seam")
}

// TestListSessions_FilterAndOrder proves per-project filtering (by the
// session's `directory`) and most-recent-first ordering. The fixture is fed
// REVERSED so a working sort is required — passing an already-sorted input
// would not exercise the ordering at all.
func TestListSessions_FilterAndOrder(t *testing.T) {
	raw := readFixture(t, "session_list.json")
	var entries []opencodeListEntry
	require.NoError(t, json.Unmarshal(raw, &entries))

	// Pick the directory that has the most sessions in the fixture, and record
	// the expected most-recent-first order from the raw `updated` timestamps.
	byDir := map[string][]opencodeListEntry{}
	for _, e := range entries {
		byDir[e.Directory] = append(byDir[e.Directory], e)
	}
	var targetDir string
	for d, es := range byDir {
		if len(es) > len(byDir[targetDir]) {
			targetDir = d
		}
	}
	require.GreaterOrEqual(t, len(byDir[targetDir]), 2, "need a multi-session directory to test ordering")

	want := append([]opencodeListEntry(nil), byDir[targetDir]...)
	sort.SliceStable(want, func(i, j int) bool { return want[i].Updated > want[j].Updated })
	wantIDs := make([]string, len(want))
	for i, e := range want {
		wantIDs[i] = e.ID
	}

	// Reverse the whole fixture array so the input is worst-case unsorted.
	rev := append([]opencodeListEntry(nil), entries...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	revBytes, err := json.Marshal(rev)
	require.NoError(t, err)

	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(revBytes, nil, nil)))
	got, err := h.ListSessions(targetDir)
	require.NoError(t, err)

	gotIDs := make([]string, len(got))
	for i, m := range got {
		gotIDs[i] = m.ID
		assert.False(t, m.EndTime.IsZero(), "updated time should be populated")
	}
	assert.Equal(t, wantIDs, gotIDs, "filtered to the target directory, most-recent-first")
}

// TestListSessions_ResolvesSymlinkedWorkDir pins a real bug: ListSessions
// compared filepath.Abs(workDir) to opencode's `directory` value by exact
// string equality, with no symlink resolution — filepath.Abs only cleans a
// path, it does not resolve symlinks — so a symlinked workDir matched zero
// sessions and returned a nil error, indistinguishable from genuine absence.
func TestListSessions_ResolvesSymlinkedWorkDir(t *testing.T) {
	real := t.TempDir()
	resolvedReal, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "link-to-project")
	require.NoError(t, os.Symlink(resolvedReal, link))

	entries := []opencodeListEntry{{ID: "ses_1", Directory: resolvedReal, Created: 1, Updated: 2}}
	raw, err := json.Marshal(entries)
	require.NoError(t, err)

	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(raw, nil, nil)))
	got, err := h.ListSessions(link)
	require.NoError(t, err)
	require.Len(t, got, 1, "the symlinked workDir must resolve to the same project opencode recorded")
	assert.Equal(t, "ses_1", got[0].ID)
}

// TestListSessions_NoSessions: an empty store and a directory with no sessions
// both return empty WITHOUT error (honest absence, never a masked failure).
func TestListSessions_NoSessions(t *testing.T) {
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning([]byte("[]"), nil, nil)))

	got, err := h.ListSessions("")
	require.NoError(t, err)
	assert.Empty(t, got)

	// opencode emits EMPTY stdout for a project with zero sessions (not "[]"):
	// honest absence, not an error.
	hEmpty := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning([]byte("  \n"), nil, nil)))
	gotEmpty, err := hEmpty.ListSessions("")
	require.NoError(t, err)
	assert.Empty(t, gotEmpty)

	// A real, non-empty store filtered to an unrelated directory is also empty.
	h2 := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(readFixture(t, "session_list.json"), nil, nil)))
	got2, err := h2.ListSessions("/nonexistent/project/dir")
	require.NoError(t, err)
	assert.Empty(t, got2)
}

// TestListSessions_Malformed: a garbled `session list` output errors LOUDLY, it
// does not degrade to an empty list (the kiro/codex silent-empty failure mode).
func TestListSessions_Malformed(t *testing.T) {
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning([]byte("not json at all"), nil, nil)))
	_, err := h.ListSessions("")
	require.Error(t, err)

	// A failed invocation (binary missing / nonzero exit) also surfaces.
	h2 := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(func(dir string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("boom")
	}))
	_, err = h2.ListSessions("")
	require.Error(t, err)
}

// TestGetSession_Marker: the real HIST-8842 export round-trips into normalized
// entries with the user prompt and the assistant marker, in order.
func TestGetSession_Marker(t *testing.T) {
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(nil, readFixture(t, "export_hist.json"), nil)))
	sess, err := h.GetSession("", "ses_09c585b01ffeH3APdv6yZvEHv4")
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.NotEmpty(t, sess.Entries)

	var userIdx, asstIdx = -1, -1
	for i, e := range sess.Entries {
		switch {
		case e.Type == agent.EntryTypeUser && userIdx == -1:
			userIdx = i
		case e.Type == agent.EntryTypeAssistant && e.Content == "HIST-8842":
			asstIdx = i
		}
	}
	require.NotEqual(t, -1, userIdx, "a user entry is present")
	require.NotEqual(t, -1, asstIdx, "the assistant HIST-8842 marker is present")
	assert.Less(t, userIdx, asstIdx, "user turn precedes the assistant reply")
	assert.Contains(t, sess.Entries[userIdx].Content, "HIST-8842", "user prompt carried through")

	// The reasoning part maps to a thinking entry, distinct from assistant text.
	hasThinking := false
	for _, e := range sess.Entries {
		if e.Type == agent.EntryTypeThinking {
			hasThinking = true
		}
	}
	assert.True(t, hasThinking, "opencode reasoning part -> thinking entry")
	assert.Equal(t, "ses_09c585b01ffeH3APdv6yZvEHv4", sess.ID)
}

// TestGetSession_Tools: a tool part expands to a tool_use + tool_result pair,
// carrying the tool name and error status.
func TestGetSession_Tools(t *testing.T) {
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(nil, readFixture(t, "export_tool.json"), nil)))
	// The id must be the fixture's own: an export carrying a DIFFERENT session
	// than the one asked for is now a hard error, so a placeholder id here
	// would be asserting the wrong contract.
	sess, err := h.GetSession("", "ses_09c85f028ffe7AzEs68oZ749iU")
	require.NoError(t, err)

	var toolUse, toolResult int
	sawRead := false
	sawError := false
	for _, e := range sess.Entries {
		switch e.Type {
		case agent.EntryTypeToolUse:
			toolUse++
			if e.ToolName == "read" {
				sawRead = true
			}
		case agent.EntryTypeToolResult:
			toolResult++
			if e.IsError {
				sawError = true
			}
		}
	}
	assert.Positive(t, toolUse, "tool_use entries emitted")
	assert.Equal(t, toolUse, toolResult, "each tool part yields a matching result")
	assert.True(t, sawRead, "tool name preserved")
	assert.True(t, sawError, "error status preserved")
}

// TestGetSession_Error: a failing export errors LOUDLY rather than returning a
// silent empty transcript.
func TestGetSession_Error(t *testing.T) {
	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(nil, nil, fmt.Errorf("no such session"))))
	_, err := h.GetSession("", "bad")
	require.Error(t, err)

	// Garbled export bytes also error.
	h2 := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(nil, []byte("{{"), nil)))
	_, err = h2.GetSession("", "bad")
	require.Error(t, err)
}

// TestGetCurrentSession_Marker: the host-side "most recent for this project"
// flow (list -> pick newest -> export) surfaces the marker session.
func TestGetCurrentSession_Marker(t *testing.T) {
	list := readFixture(t, "session_list.json")
	// The ocprobe directory holds exactly the HIST-8842 session in the fixture.
	var entries []opencodeListEntry
	require.NoError(t, json.Unmarshal(list, &entries))
	var probeDir string
	for _, e := range entries {
		if e.ID == "ses_09c585b01ffeH3APdv6yZvEHv4" {
			probeDir = e.Directory
		}
	}
	require.NotEmpty(t, probeDir)

	h := newOpencodeSessionHistory(nil, WithOpencodeSessionRunner(runnerReturning(list, readFixture(t, "export_hist.json"), nil)))
	sess, err := h.GetCurrentSession(probeDir)
	require.NoError(t, err)
	require.NotNil(t, sess)

	found := false
	for _, e := range sess.Entries {
		if e.Type == agent.EntryTypeAssistant && e.Content == "HIST-8842" {
			found = true
		}
	}
	assert.True(t, found, "current-session read surfaces the HIST-8842 marker")
}

// TestGetSessionByPath_Unsupported: opencode has no file-backed transcript, so
// path-based reads fail loudly instead of returning empty.
func TestGetSessionByPath_Unsupported(t *testing.T) {
	h := newOpencodeSessionHistory(nil)
	_, err := h.GetSessionByPath("/some/path.jsonl")
	require.Error(t, err)
	assert.Empty(t, h.TranscriptPathFromHook("wd", "sid", "/some/path.jsonl"))
}
