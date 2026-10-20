package cli

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// `session remove` destroys two artifacts: the index entry and the
// transcript. Every test below asserts BOTH, because a version that drops the
// index entry and leaves the bytes on disk satisfies any assertion that checks
// only the entry — and that is the shape a reader would call correct at a
// glance. It never reaches the output dir, which is the human's.

// TestSessionRemove_ReportLeavesEverything is the report side.
func TestSessionRemove_ReportLeavesEverything(t *testing.T) {
	releaseTranscriptRetention(t)
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	leftover := seedLeftoverSummary(t, harp)
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "remove", harp)
	require.NoError(t, err)

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.NotNil(t, entry, "a report must not drop the index entry")
	assert.True(t, onDisk(t, transcript), "a report must not destroy the transcript")
	assert.True(t, onDisk(t, leftover), "a report must not touch the output dir")
	assert.Contains(t, stdout+stderr, "removed nothing")
	assert.Contains(t, stdout+stderr, "session remove "+harp+" --yes")
}

// TestSessionRemove_YesDestroysTheEntryAndTheTranscript is the apply side, and the
// whole point of this change. Asserting only the index entry is what made the
// old behaviour look correct.
func TestSessionRemove_YesDestroysTheEntryAndTheTranscript(t *testing.T) {
	releaseTranscriptRetention(t)
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	leftover := seedLeftoverSummary(t, harp)
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	out, err := execRootCmd(t, "session", "remove", harp, "--yes", "--format", "json")
	require.NoError(t, err)

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.Nil(t, entry, "the index entry must be gone")
	assert.False(t, onDisk(t, transcript), "the transcript must be gone")
	assert.True(t, onDisk(t, leftover), "the output dir is the human's: a summary an older ctxloom left there stays")

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
	assert.Positive(t, got.Files.BytesFreed, "a removal that freed zero bytes destroyed no files")
}

// TestSessionRemove_LeavesAuthoredWorkAlone: authored content in a harp
// directory is never destroyed by anything, remove included. It is named in
// the report instead, so a kept file is never silently kept.
func TestSessionRemove_LeavesAuthoredWorkAlone(t *testing.T) {
	releaseTranscriptRetention(t)
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	seedTranscript(t, harp)
	seedLeftoverSummary(t, harp)
	notes := seedAuthoredNote(t, harp, "design-notes.md")
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	out, err := execRootCmd(t, "session", "remove", harp, "--yes")
	require.NoError(t, err)

	assert.True(t, onDisk(t, notes), "authored work is never destroyed")
	assert.Contains(t, out, "design-notes.md", "and it must be named in the report")
}

// TestSessionRemove_AfterTheTranscriptIsGone_Succeeds pins that retention
// guards a real file. Once the transcript is gone there is nothing left to
// protect, so remove proceeds even for a session that ended within the
// retention period — rather than refusing forever over a file that no longer
// exists.
func TestSessionRemove_AfterTheTranscriptIsGone_Succeeds(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	t.Cleanup(func() { resetFlags(t, rootCmd) })
	require.NoError(t, os.Remove(transcript))

	_, err := execRootCmd(t, "session", "remove", harp, "--yes")
	require.NoError(t, err, "with the transcript already gone, retention has nothing to protect")

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	assert.Nil(t, entry)
}
