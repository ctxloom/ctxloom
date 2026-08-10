package cli

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Both sides of every destroyer, always. The report side asserts the thing is
// STILL THERE afterwards; the apply side asserts it is GONE. They fail in
// opposite directions, and this package has shipped each failure: a report
// that quietly destroyed, and a --yes that quietly did nothing. One of the two
// alone certifies neither.

// resetSessionPurgeFlags restores the package-level flag vars behind every
// destroyer under `session`. pflag never un-sets a flag a prior invocation
// set, so a --yes left on turns a later report-only test into an apply — the
// exact leak these tests exist to catch, arriving from the test harness
// instead of from the product.
func resetSessionPurgeFlags(t *testing.T) {
	t.Helper()
	sessionPurgeYes = false
	sessionTranscriptPurgeYes = false
	sessionTranscriptPurgeUndistilled = false
	sessionArtifactsPurgeYes = false
	sessionWorktreesPurgeYes = false
	resetRootFormat(t)
}

type purgePayload struct {
	Harp    string `json:"harp"`
	Applied bool   `json:"applied"`
	Destroy []struct {
		Rel   string `json:"rel"`
		Class string `json:"class"`
	} `json:"destroy"`
	Keep []struct {
		Rel   string `json:"rel"`
		Class string `json:"class"`
	} `json:"keep"`
	BytesFreed int64 `json:"bytes_freed"`
}

// --- session transcript purge ----------------------------------------------

// TestTranscriptPurge_ReportLeavesEverythingOnDisk is the report side. The
// assertion that matters is the one on DISK: a plan that names the file it
// would destroy, having already destroyed it, renders identically.
func TestTranscriptPurge_ReportLeavesEverythingOnDisk(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "transcript", "purge", harp)
	require.NoError(t, err)

	assert.True(t, onDisk(t, transcript), "a report must not destroy the transcript")
	assert.True(t, onDisk(t, essence), "a report must not destroy the essence")
	assert.Contains(t, stdout+stderr, "removed nothing",
		"the report must say outright that it removed nothing")
	assert.Contains(t, stdout+stderr, "session transcript purge "+harp+" --yes",
		"the report must name the exact command that applies it")
}

// TestTranscriptPurge_YesDestroysTheTranscriptOnly is the apply side.
func TestTranscriptPurge_YesDestroysTheTranscriptOnly(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	out, err := execRootCmd(t, "session", "transcript", "purge", harp, "--yes", "--format", "json")
	require.NoError(t, err)

	assert.False(t, onDisk(t, transcript), "--yes must destroy the transcript")
	assert.True(t, onDisk(t, essence), "the transcript's destroyer must not reach the essence")

	var got purgePayload
	require.NoError(t, json.Unmarshal([]byte(out), &got), "output must be clean JSON: %s", out)
	assert.True(t, got.Applied)
	assert.Positive(t, got.BytesFreed, "a purge that freed zero bytes destroyed nothing")
}

// TestTranscriptPurge_NeverDistilledRefuses: the transcript of a session with
// no essence is the ONLY record of what happened, so destroying it takes the
// extra deliberate flag. --undistilled lives here, on the leaf that
// understands the question, and never on the parent.
func TestTranscriptPurge_NeverDistilledRefuses(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	_, stderr, err := execRootCmdBoth(t, "session", "transcript", "purge", harp, "--yes")
	require.Error(t, err, "destroying the only record of a session must refuse")
	assert.True(t, onDisk(t, transcript), "a refusal must leave the transcript alone")
	assert.Contains(t, stderr, "--undistilled")

	resetSessionPurgeFlags(t)
	_, err = execRootCmd(t, "session", "transcript", "purge", harp, "--undistilled", "--yes")
	require.NoError(t, err)
	assert.False(t, onDisk(t, transcript), "--undistilled --yes destroys it anyway")
}

// --- session artifacts purge -----------------------------------------------

func TestArtifactsPurge_ReportLeavesTheEssenceOnDisk(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "artifacts", "purge", harp)
	require.NoError(t, err)

	assert.True(t, onDisk(t, essence), "a report must not destroy the essence")
	assert.True(t, onDisk(t, transcript))
	assert.Contains(t, stdout+stderr, "removed nothing")
	assert.Contains(t, stdout+stderr, "session artifacts purge "+harp+" --yes")
}

func TestArtifactsPurge_YesDestroysTheEssenceOnly(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	_, err := execRootCmd(t, "session", "artifacts", "purge", harp, "--yes")
	require.NoError(t, err)

	assert.False(t, onDisk(t, essence), "--yes must destroy the essence")
	assert.True(t, onDisk(t, transcript), "the essence's destroyer must not reach the transcript")
}

// --- session purge (the sweep) ---------------------------------------------

func TestSessionPurge_ReportLeavesAllThreePopulationsOnDisk(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "purge", harp)
	require.NoError(t, err)

	assert.True(t, onDisk(t, transcript))
	assert.True(t, onDisk(t, essence))
	assert.Contains(t, stdout+stderr, "removed nothing")
	assert.Contains(t, stdout+stderr, "session purge "+harp+" --yes")
}

func TestSessionPurge_YesSweepsTranscriptAndArtifacts(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	essence := seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	_, err := execRootCmd(t, "session", "purge", harp, "--yes")
	require.NoError(t, err)

	assert.False(t, onDisk(t, transcript), "the sweep destroys the transcript")
	assert.False(t, onDisk(t, essence), "the sweep destroys the artifacts too")
}

// TestSessionPurge_KeepsTheIndexEntry pins the boundary between the two
// destroyers: purge empties a session, delete removes it. A purged session
// stays listed — marked, not gone — so an operator can still see it existed.
func TestSessionPurge_KeepsTheIndexEntry(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, harp := seedEndedSession(t, dir, "claude-code")
	seedTranscript(t, harp)
	seedEssence(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	_, err := execRootCmd(t, "session", "purge", harp, "--yes")
	require.NoError(t, err)

	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	require.NotNil(t, entry, "purge empties a session; it does not unlist it")
	assert.NotNil(t, entry.PurgedAt, "the index records that the bulk was destroyed")
}

// TestSessionPurge_UndistilledIsNotOnTheParent: selection flags live on the
// leaf that understands them. The sweep still cannot destroy the only record
// of an undistilled session — it refuses and names the leaf that can.
func TestSessionPurge_UndistilledIsNotOnTheParent(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, harp := seedEndedSession(t, dir, "claude-code")
	transcript := seedTranscript(t, harp)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	assert.Nil(t, sessionPurgeCmd.Flags().Lookup("undistilled"),
		"--undistilled belongs to `session transcript purge`, not to the sweep")
	assert.Nil(t, sessionPurgeCmd.Flags().Lookup("everything"),
		"--everything is replaced by the fan-out; the sweep needs no scope flag")

	_, stderr, err := execRootCmdBoth(t, "session", "purge", harp, "--yes")
	require.Error(t, err)
	assert.True(t, onDisk(t, transcript))
	assert.Contains(t, stderr, "session transcript purge",
		"the refusal must name the leaf that can do what was asked")
}

// TestSessionPurge_LiveSessionRefuses keeps the oldest guard: a session with
// no ended_at may still be writing its own transcript.
func TestSessionPurge_LiveSessionRefuses(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)
	transcript := seedTranscript(t, entry.HarpName)
	seedEssence(t, entry.HarpName)
	t.Cleanup(func() { resetSessionPurgeFlags(t) })

	_, stderr, err := execRootCmdBoth(t, "session", "purge", entry.HarpName, "--yes")
	require.Error(t, err)
	assert.True(t, onDisk(t, transcript), "a live session's transcript is never destroyed")
	assert.Contains(t, stderr, "still live")
}

// --- session worktrees -----------------------------------------------------

// TestSessionWorktrees_IsANamespaceThatLists pins the sub-noun shape and the
// retirement of --reap/--harp: the flags converge on `purge` and a positional.
func TestSessionWorktrees_IsANamespaceThatLists(t *testing.T) {
	child, ok := groupNodeDefaultChild(sessionWorktreesCmd)
	require.True(t, ok, "`session worktrees` must answer its bare form")
	assert.Equal(t, "list", child)
	assert.Nil(t, sessionWorktreesCmd.Flags().Lookup("reap"),
		"--reap is now the `purge` leaf")
	assert.Nil(t, sessionWorktreesCmd.Flags().Lookup("harp"),
		"--harp is now a positional")

	var purge bool
	for _, c := range sessionWorktreesCmd.Commands() {
		if c.Name() == "purge" {
			purge = true
		}
	}
	assert.True(t, purge, "`session worktrees purge` must exist")
}
