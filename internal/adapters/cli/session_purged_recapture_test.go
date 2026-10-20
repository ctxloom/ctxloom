package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// A purged session's transcript is re-captured only on an explicit act — a
// `session adopt` or the exit of a new interactive run of that harp — never
// by a read. Each case below starts from a purged session whose engine log
// outlived the purge.

// seedPurgedClaudeSession is seedGrownClaudeSession's session, purged: marked
// purged and its canonical transcript removed, its vendor log left in place.
// It returns the harp and its canonical transcript's path.
func seedPurgedClaudeSession(t *testing.T, projectDir string) (harp, canon string) {
	t.Helper()
	harp = seedGrownClaudeSession(t, projectDir, 6)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	require.NoError(t, mgr.MarkPurged(harp, time.Now().UTC()))
	canon, err = paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(canon))
	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	require.NotNil(t, entry)
	_, err = os.Stat(entry.TranscriptPath)
	require.NoError(t, err, "the engine log outlives the purge")
	return harp, canon
}

func assertNotRecaptured(t *testing.T, canon, by string) {
	t.Helper()
	_, err := os.Stat(canon)
	assert.True(t, os.IsNotExist(err), "%s must not re-capture a purged session's transcript", by)
}

func assertRecaptured(t *testing.T, canon, by string) {
	t.Helper()
	_, err := os.Stat(canon)
	assert.NoError(t, err, "%s re-captures a purged session's transcript", by)
}

// assertNoLongerPurged pins what a re-capture makes of a purged session: an
// ordinary one again. Its record carries no purge mark, `session list` does
// not show it as purged, and a read refreshes its transcript once more.
func assertNoLongerPurged(t *testing.T, harp, canon, by string) {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.Nil(t, entry.PurgedAt, "%s clears the purge mark", by)

	out, err := execRootCmd(t, "session", "list", "--format", "json")
	require.NoError(t, err)
	assert.NotContains(t, jsonRowsByHarp(t, out)[harp], "purged", "after %s, session list shows an ordinary session", by)

	require.NoError(t, os.Remove(canon))
	_, err = execRootCmd(t, "session", "show", harp, "--format", "json")
	require.NoError(t, err)
	assertRecaptured(t, canon, "a read after "+by)
}

// assertStillPurged pins that a re-capture that rebuilt nothing leaves the
// purge mark where it was.
func assertStillPurged(t *testing.T, harp, by string) {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	require.NotNil(t, entry)
	assert.NotNil(t, entry.PurgedAt, "%s rebuilt nothing, so the session stays marked purged", by)
}

func TestPurgedSession_AdoptRecapturesIt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	// An earlier session the harp lost track of: the fixture's conversation,
	// a year before the live one.
	whole, err := os.ReadFile(claudeVendorFixturePath(t))
	require.NoError(t, err)
	orphan := strings.ReplaceAll(string(whole), `"timestamp":"2026-`, `"timestamp":"2025-`)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(entry.TranscriptPath), "id-orphan.jsonl"), []byte(orphan), 0o644))
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	stdout, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--yes", "--format", "text")
	require.NoError(t, err, stderr)
	require.Contains(t, stdout, "id-orphan", "the earlier session is the one adopted")
	assertRecaptured(t, canon, "session adopt")
	assertNoLongerPurged(t, harp, canon, "session adopt")
}

// With no earlier log to adopt, adopt still rebuilds a purged session's
// transcript from the engine log that survives, and says so.
func TestPurgedSession_AdoptWithNothingToAdoptRecapturesIt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	_, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--yes", "--format", "text")
	require.NoError(t, err, stderr)
	assert.Contains(t, stderr, fmt.Sprintf(adoptPurgedRebuilt, harp))
	assertRecaptured(t, canon, "session adopt with nothing to adopt")
	assertNoLongerPurged(t, harp, canon, "session adopt with nothing to adopt")
}

// adopt's report on a purged session says that --yes would rebuild it.
func TestPurgedSession_AdoptReportSaysItWouldRebuild(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	_, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--format", "text")
	require.NoError(t, err, stderr)
	assert.Contains(t, stderr, fmt.Sprintf(adoptPlanPurged, harp))
	assertNotRecaptured(t, canon, "session adopt's report")
	assertStillPurged(t, harp, "session adopt's report")
}

// A purged session whose engine log did not survive has nothing to rebuild
// from: adopt says so, and the session stays purged.
func TestPurgedSession_AdoptWithNoEngineLogKeepsTheMark(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.Find(harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(entry.TranscriptPath))
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	_, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--yes", "--format", "text")
	requireAdoptFailed(t, err, stderr)
	assert.Contains(t, stderr, fmt.Sprintf(adoptPurgedNothingToRebuild, harp))
	assertNotRecaptured(t, canon, "session adopt with no engine log")
	assertStillPurged(t, harp, "session adopt with no engine log")
}

// A rebuild that fails says why, and the session stays purged.
func TestPurgedSession_AdoptWhoseRebuildFailsKeepsTheMark(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	refuseRebuild(t, harp)
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	_, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--yes", "--format", "text")
	requireAdoptFailed(t, err, stderr)
	_, healErr := healAdopted(context.Background(), harp)
	require.Error(t, healErr)
	assert.Contains(t, stderr, fmt.Sprintf(adoptPurgedNotRebuilt, harp, healErr))
	assertNotRecaptured(t, canon, "a failed session adopt")
	assertStillPurged(t, harp, "a failed session adopt")
}

// A rebuild that lands while the purge mark cannot be cleared is said as
// exactly that: the transcript was rebuilt, and the session is still purged.
func TestPurgedSession_AdoptThatCannotClearTheMarkSaysItRebuilt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	// The sidecar lock's path taken by a directory: every sidecar write fails,
	// and a nothing-to-adopt adopt writes the sidecar only to clear the mark.
	lock, err := paths.HarpSidecarLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(lock))
	require.NoError(t, os.Mkdir(lock, 0o755))
	t.Cleanup(func() { resetFlags(t, rootCmd) })

	_, stderr, err := execRootCmdBoth(t, "session", "adopt", harp, "--yes", "--format", "text")
	requireAdoptFailed(t, err, stderr)
	rebuiltStillMarked := strings.SplitN(adoptPurgedRebuiltStillMarked, "%v", 2)[0]
	assert.Contains(t, stderr, fmt.Sprintf(rebuiltStillMarked, harp))
	assert.NotContains(t, stderr, fmt.Sprintf(strings.SplitN(adoptPurgedNotRebuilt, "%v", 2)[0], harp))
	assertRecaptured(t, canon, "a session adopt that cannot clear the mark")
	assertStillPurged(t, harp, "a session adopt that cannot clear the mark")
}

func TestPurgedSession_AnInteractiveExitRecapturesIt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)

	healVendorTranscriptOnExit(harp)
	assertRecaptured(t, canon, "an interactive run's exit")
	assertNoLongerPurged(t, harp, canon, "an interactive run's exit")
}

func TestPurgedSession_AnInteractiveExitWhoseRebuildFailsKeepsTheMark(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	refuseRebuild(t, harp)

	healVendorTranscriptOnExit(harp)
	assertNotRecaptured(t, canon, "a failed interactive exit")
	assertStillPurged(t, harp, "a failed interactive exit")
}

// requireAdoptFailed pins that an adopt whose rebuild failed exits 1, and
// silently: the message it printed is the whole report.
func requireAdoptFailed(t *testing.T, err error, stderr string) {
	t.Helper()
	var status clifmt.ExitStatus
	require.ErrorAs(t, err, &status, stderr)
	assert.Equal(t, 1, status.Code)
}

// refuseRebuild records an engine version no transcript reader is validated
// for, so every rebuild of harp is refused (vendorreader.SelectAdapter).
func refuseRebuild(t *testing.T, harp string) {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	require.NoError(t, mgr.RecordEngineVersion(harp, "0.0.1"))
}

func TestPurgedSession_ShowDoesNotRecaptureIt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)

	_, err := execRootCmd(t, "session", "show", harp, "--format", "json")
	require.NoError(t, err)
	assertNotRecaptured(t, canon, "session show")
}

func TestPurgedSession_ListDoesNotRecaptureIt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	_, canon := seedPurgedClaudeSession(t, dir)

	_, err := runSessionCmd(t, "session", "list", "--format", "json")
	require.NoError(t, err)
	assertNotRecaptured(t, canon, "session list")
}

func TestPurgedSession_ResumeDoesNotRecaptureIt(t *testing.T) {
	dir := runCLIFixture(t)
	harp, canon := seedPurgedClaudeSession(t, dir)
	runResumeSession, runDryRun = harp, false
	t.Cleanup(func() { runResumeSession = "" })

	(&runState{ctx: context.Background()}).resumedTranscript()
	assertNotRecaptured(t, canon, "run --session's resume")
}

func TestPurgedSession_DryRunResumeDoesNotRecaptureIt(t *testing.T) {
	dir := runCLIFixture(t)
	harp, canon := seedPurgedClaudeSession(t, dir)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "--session", harp, "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	assertNotRecaptured(t, canon, "run --session --dry-run")
	assert.NotContains(t, res.stderr, dryRunTranscriptMayBeStale, "a real run would not refresh it either")
}
