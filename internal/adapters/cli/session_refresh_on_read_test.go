package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// grownTitle is the claude fixture's first main-thread user prompt; it lies
// past the fixture's first three lines.
const grownTitle = "GOAL: Truthfulness audit"

// grownTurn is a turn past the fixture's first six lines.
const grownTurn = "[Request interrupted by user for tool use]"

// seedGrownClaudeSession indexes a claude session in projectDir bound to a
// vendor log holding the fixture's first captured lines, captures it once
// through the exit seam, then grows the log to the whole fixture: a session
// whose later run did not exit through ctxloom.
func seedGrownClaudeSession(t *testing.T, projectDir string, captured int) string {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, "claude-code")

	whole, err := os.ReadFile(claudeVendorFixturePath(t))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(whole), "\n"), "\n")
	require.Greater(t, len(lines), captured)
	vendorPath := filepath.Join(t.TempDir(), "vendor.jsonl")
	require.NoError(t, os.WriteFile(vendorPath, []byte(strings.Join(lines[:captured], "\n")+"\n"), 0o644))
	require.NoError(t, mgr.BindSession(entry.HarpName, "sess-1", vendorPath))

	healVendorTranscriptOnExit(entry.HarpName)
	require.NoError(t, os.WriteFile(vendorPath, whole, 0o644))
	return entry.HarpName
}

// `session show` titles a session from its engine's log as it is now, not
// from a last capture that held no prompt yet.
func TestSessionShow_TitlesFromTheVendorLogGrownSinceTheLastCapture(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp := seedGrownClaudeSession(t, dir, 3)

	out, err := execRootCmd(t, "session", "show", harp, "--format", "json")
	require.NoError(t, err)
	var got operations.SessionView
	require.NoError(t, json.Unmarshal([]byte(out), &got), out)
	assert.True(t, strings.HasPrefix(got.Title, grownTitle), "got title %q", got.Title)
}

// `session list` titles an untitled row from its engine's log as it is now.
func TestSessionList_TitlesFromTheVendorLogGrownSinceTheLastCapture(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp := seedGrownClaudeSession(t, dir, 3)

	out, err := runSessionCmd(t, "session", "list", "--format", "json")
	require.NoError(t, err)
	rows := jsonRowsByHarp(t, out)
	summary, _ := rows[harp]["summary"].(string)
	assert.True(t, strings.HasPrefix(summary, grownTitle), "got summary %q", summary)
}

// `session search` matches a title its last capture did not hold yet.
func TestSessionSearch_MatchesATitleTheVendorLogGrewSinceTheLastCapture(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp := seedGrownClaudeSession(t, dir, 3)

	out, err := runSessionCmd(t, "session", "search", "Truthfulness", "--format", "json")
	require.NoError(t, err)
	rows := jsonRowsByHarp(t, out)
	assert.Contains(t, rows, harp)
}

// A real `run --session` primes the resumed run with the turns its engine
// logged after the session's last capture: the lead a launch composes
// (resumedTranscript) is built over the refreshed transcript.
func TestRunCharacterization_ResumeSessionReadsTheTurnsLoggedSinceTheLastCapture(t *testing.T) {
	dir := runCLIFixture(t)
	harp := seedGrownClaudeSession(t, dir, 6)
	runResumeSession, runDryRun = harp, false
	t.Cleanup(func() { runResumeSession = "" })

	lead := (&runState{ctx: context.Background()}).resumedTranscript()
	require.Len(t, lead, 1)
	assert.Contains(t, lead[0].Body, grownTurn, "the resume reads the session as its engine logged it")
}

// `run --session --dry-run` previews the transcript the session has and
// writes nothing: the canonical file is byte-identical with its mtime
// unchanged, the turns logged since are not in the preview, and the preview
// says its copy may be stale.
func TestRunCharacterization_DryRunResumeReadsTheCaptureAndWritesNothing(t *testing.T) {
	dir := runCLIFixture(t)
	harp := seedGrownClaudeSession(t, dir, 6)
	canon, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	old := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(canon, old, old))
	before, err := os.ReadFile(canon)
	require.NoError(t, err)
	beforeInfo, err := os.Stat(canon)
	require.NoError(t, err)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "--session", harp, "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got))
	assert.NotContains(t, got.Context, grownTurn, "a preview reads the last capture")
	assert.Contains(t, got.Context, "## Resumed session "+harp, "the capture still leads the preview: %s", got.Context)
	assert.Contains(t, res.stderr, dryRunTranscriptMayBeStale)

	after, err := os.ReadFile(canon)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a dry run must not rewrite the transcript")
	afterInfo, err := os.Stat(canon)
	require.NoError(t, err)
	assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), "a dry run must not touch the transcript")
}
