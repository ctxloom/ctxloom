package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetSessionTranscriptFlags restores the package-level flag vars behind
// `session transcript`, for the reason session_worktrees_test.go documents:
// pflag never un-sets a flag a prior invocation set, so a --all left on leaks
// into a later run that never mentions it.
func resetSessionTranscriptFlags() {
	sessionTranscriptListAll = false
	clidiag.SetStructured(false)
}

type transcriptListPayload struct {
	Transcripts []struct {
		Harp     string `json:"harp"`
		Captured bool   `json:"captured"`
		Bytes    int64  `json:"bytes"`
		Path     string `json:"path"`
	} `json:"transcripts"`
}

// TestSessionTranscript_BareFormLists pins the sub-noun's shape: `session
// transcript` is a namespace whose bare form answers with the listing, the
// same seam `remote` uses. A namespace that printed help here would make the
// most-typed spelling the least useful one.
func TestSessionTranscript_BareFormLists(t *testing.T) {
	child, ok := groupNodeDefaultChild(sessionTranscriptCmd)
	require.True(t, ok, "`session transcript` must declare a default child")
	assert.Equal(t, "list", child)

	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	seedTranscript(t, harp)
	t.Cleanup(resetSessionTranscriptFlags)

	out, err := execRootCmd(t, "session", "transcript", "--format", "json")
	require.NoError(t, err)
	var got transcriptListPayload
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	require.Len(t, got.Transcripts, 1)
	assert.Equal(t, harp, got.Transcripts[0].Harp)
}

// TestSessionTranscriptList_ReportsRealBytes is the payload assertion. A
// listing that reports a captured transcript must report its actual size:
// "captured" with a zero byte count is exactly the shape that lets an empty
// file pass for a recorded conversation.
func TestSessionTranscriptList_ReportsRealBytes(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	path := seedTranscript(t, harp)
	t.Cleanup(resetSessionTranscriptFlags)

	info, err := os.Stat(path)
	require.NoError(t, err)

	out, err := execRootCmd(t, "session", "transcript", "list", "--format", "json")
	require.NoError(t, err)
	var got transcriptListPayload
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	require.Len(t, got.Transcripts, 1)
	assert.True(t, got.Transcripts[0].Captured)
	assert.Equal(t, info.Size(), got.Transcripts[0].Bytes)
	assert.Equal(t, path, got.Transcripts[0].Path)
}

// TestSessionTranscriptList_UncapturedSessionIsStillNamed keeps the listing
// honest about absence. A session whose transcript was never captured is a
// row saying so, not a missing row: omitting it makes "nothing was captured"
// indistinguishable from "there are no sessions".
func TestSessionTranscriptList_UncapturedSessionIsStillNamed(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	t.Cleanup(resetSessionTranscriptFlags)

	out, err := execRootCmd(t, "session", "transcript", "list", "--format", "json")
	require.NoError(t, err)
	var got transcriptListPayload
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	require.Len(t, got.Transcripts, 1)
	assert.Equal(t, harp, got.Transcripts[0].Harp)
	assert.False(t, got.Transcripts[0].Captured)
	assert.Zero(t, got.Transcripts[0].Bytes)
}

// TestSessionTranscriptList_PositionalHarpRestricts pins the arity the whole
// noun shares: the harp is a positional, never a flag.
func TestSessionTranscriptList_PositionalHarpRestricts(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, first := seedEndedSession(t, dir, "claude-code")
	seedTranscript(t, first)
	_, second := seedEndedSession(t, dir, "claude-code")
	seedTranscript(t, second)
	t.Cleanup(resetSessionTranscriptFlags)

	out, err := execRootCmd(t, "session", "transcript", "list", second, "--format", "json")
	require.NoError(t, err)
	var got transcriptListPayload
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	require.Len(t, got.Transcripts, 1)
	assert.Equal(t, second, got.Transcripts[0].Harp)
}

// TestSessionTranscriptList_UnknownHarpFails matches the rest of the noun:
// naming a harp nothing knows is an error, not an empty success.
func TestSessionTranscriptList_UnknownHarpFails(t *testing.T) {
	testsupport.ProjectDir(t)
	t.Cleanup(resetSessionTranscriptFlags)

	_, err := execRootCmd(t, "session", "transcript", "list", "no-such-harp")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no-such-harp")
}

// TestSessionWatch_MovedUnderTranscript pins the move: watching is something
// you do to a session's TRANSCRIPT, so it lives under that sub-noun and
// nowhere else.
func TestSessionWatch_MovedUnderTranscript(t *testing.T) {
	for _, c := range sessionCmd.Commands() {
		assert.NotEqual(t, "watch", c.Name(), "`session watch` moved to `session transcript watch`")
	}
	var found bool
	for _, c := range sessionTranscriptCmd.Commands() {
		if c.Name() == "watch" {
			found = true
		}
	}
	assert.True(t, found, "`session transcript watch` must exist")
}
