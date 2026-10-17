package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// `session keep <name>` marks the session's transcript kept at any age, the
// listing shows it, a purge refuses it naming the way out, and `--undo`
// releases it.
func TestSessionKeep_MarksShowsRefusesAndUndoes(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)

	_, err := execRootCmd(t, "session", "keep", harp)
	require.NoError(t, err)
	e, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.True(t, e.Keep, "the mark is the sidecar's")

	out, err := runSessionCmd(t, "session", "list", "--format", "json")
	require.NoError(t, err)
	assert.Equal(t, true, jsonRowsByHarp(t, out)[harp]["kept"])
	out, err = runSessionCmd(t, "session", "list", "--format", "text")
	require.NoError(t, err)
	assert.Contains(t, out, "kept")

	_, stderr, err := execRootCmdBoth(t, "session", "transcript", "purge", harp, "--yes")
	require.Error(t, err)
	assert.Contains(t, stderr, "ctxloom session keep "+harp+" --undo")
	assert.True(t, onDisk(t, transcript))

	_, err = execRootCmd(t, "session", "keep", harp, "--undo")
	require.NoError(t, err)
	e, err = mgr.Find(harp)
	require.NoError(t, err)
	assert.False(t, e.Keep)
}

// A session that ended within session_transcript_retention keeps its
// transcript from every destroyer, and the refusal is not the keep one.
func TestSessionTranscriptPurge_RefusesWithinRetention(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code") // ended now: within the default 30d
	transcript := seedTranscript(t, harp)

	for _, args := range [][]string{
		{"session", "transcript", "purge", harp, "--yes"},
		{"session", "purge", harp, "--yes"},
		{"session", "remove", harp, "--yes"},
	} {
		_, stderr, err := execRootCmdBoth(t, args...)
		require.Error(t, err, "%v", args)
		assert.Contains(t, stderr, "retained", "%v", args)
		assert.NotContains(t, stderr, "--undo", "%v", args)
		assert.True(t, onDisk(t, transcript), "%v", args)
	}
	got, err := operations.GetSession(harp)
	require.NoError(t, err)
	assert.NotNil(t, got, "remove refused: the index entry stays")
}

// `session keep` of a harp nobody recorded is an error naming it.
func TestSessionKeep_UnknownSessionErrors(t *testing.T) {
	testsupport.ProjectDir(t)
	_, err := execRootCmd(t, "session", "keep", "no-such-harp-here")
	require.ErrorIs(t, err, sessions.ErrNotFound)
}
