package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
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

// `run --session` primes the resumed run with the turns its engine logged
// after the session's last capture.
func TestRunCharacterization_ResumeSessionReadsTheTurnsLoggedSinceTheLastCapture(t *testing.T) {
	dir := runCLIFixture(t)
	harp := seedGrownClaudeSession(t, dir, 6)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "--session", harp, "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got))
	assert.Contains(t, got.Context, grownTurn, "the resume reads the session as its engine logged it")
}
