package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// resetSessionDeleteFlags restores the flag var behind `session delete`, for
// the reason session_worktrees_test.go documents at length: pflag never
// un-sets a flag a prior invocation set, so a leftover --yes would turn a
// report-only test into an apply.
func resetSessionDeleteFlags(t *testing.T) {
	t.Helper()
	sessionDeleteYes = false
	resetRootFormat(t)
}

// `session delete` is the one row here that was never a thin rename. It used
// to drop the index entry and stop, leaving the transcript and the essence on
// disk — and a test that asserted only the index entry made that look
// correct. Every test below asserts ALL THREE artifacts, because any two of
// them passing is exactly the state that shipped.

// TestSessionDelete_ReportLeavesAllThreeArtifacts is the report side.
func TestSessionDelete_ReportLeavesAllThreeArtifacts(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionDeleteFlags(t) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "delete", harp)
	require.NoError(t, err)

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.NotNil(t, entry, "a report must not drop the index entry")
	assert.True(t, onDisk(t, transcript), "a report must not destroy the transcript")
	assert.True(t, onDisk(t, essence), "a report must not destroy the essence")
	assert.Contains(t, stdout+stderr, "removed nothing")
	assert.Contains(t, stdout+stderr, "session delete "+harp+" --yes")
}

// TestSessionDelete_YesDestroysAllThreeArtifacts is the apply side, and the
// whole point of this change. Asserting only the index entry is what made the
// old behaviour look correct.
func TestSessionDelete_YesDestroysAllThreeArtifacts(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionDeleteFlags(t) })

	out, err := execRootCmd(t, "session", "delete", harp, "--yes", "--format", "json")
	require.NoError(t, err)

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.Nil(t, entry, "the index entry must be gone")
	assert.False(t, onDisk(t, transcript), "the transcript must be gone")
	assert.False(t, onDisk(t, essence), "the essence must be gone")

	var got struct {
		Harp              string `json:"harp"`
		Applied           bool   `json:"applied"`
		IndexEntryRemoved bool   `json:"index_entry_removed"`
		Files             struct {
			BytesFreed int64 `json:"bytes_freed"`
		} `json:"files"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	assert.True(t, got.Applied)
	assert.True(t, got.IndexEntryRemoved)
	assert.Positive(t, got.Files.BytesFreed, "a delete that freed zero bytes destroyed no files")
}

// TestSessionDelete_LeavesAuthoredWorkAlone: authored content in a harp
// directory is never destroyed by anything, delete included. It is named in
// the report instead, so a kept file is never silently kept.
func TestSessionDelete_LeavesAuthoredWorkAlone(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	seedTranscript(t, harp)
	seedEssence(t, harp)
	notes := seedAuthoredNote(t, harp, "design-notes.md")
	t.Cleanup(func() { resetSessionDeleteFlags(t) })

	out, err := execRootCmd(t, "session", "delete", harp, "--yes")
	require.NoError(t, err)

	assert.True(t, onDisk(t, notes), "authored work is never destroyed")
	assert.Contains(t, out, "design-notes.md", "and it must be named in the report")
}

// TestSessionDelete_UndistilledRefuses: deleting a session that was never
// distilled would destroy the only record of what happened. It refuses, and
// names the leaf that can do it deliberately.
func TestSessionDelete_UndistilledRefuses(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	t.Cleanup(func() { resetSessionDeleteFlags(t) })

	_, stderr, err := execRootCmdBoth(t, "session", "delete", harp, "--yes")
	require.Error(t, err)

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.NotNil(t, entry, "a refusal must leave the index entry")
	assert.True(t, onDisk(t, transcript), "a refusal must leave the transcript")
	assert.Contains(t, stderr, "--undistilled")
}

// TestSessionDelete_AfterAnUndistilledTranscriptPurge_Succeeds pins that the
// refusal above is an obstacle a caller can actually get past. The guard asks
// "is there a transcript here that is the only record", so once the
// transcript is deliberately gone there is nothing left to protect and delete
// proceeds — rather than refusing forever over a file that no longer exists.
func TestSessionDelete_AfterAnUndistilledTranscriptPurge_Succeeds(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	t.Cleanup(func() { resetSessionDeleteFlags(t) })

	_, err := execRootCmd(t, "session", "transcript", "purge", harp, "--undistilled", "--yes")
	require.NoError(t, err)
	require.False(t, onDisk(t, transcript))
	resetSessionPurgeFlags(t)

	_, err = execRootCmd(t, "session", "delete", harp, "--yes")
	require.NoError(t, err, "with the only record already deliberately destroyed, nothing is left to protect")

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.Nil(t, entry)
}
