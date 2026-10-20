package cli

import (
	"context"
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
}

func TestPurgedSession_AnInteractiveExitRecapturesIt(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	harp, canon := seedPurgedClaudeSession(t, dir)

	healVendorTranscriptOnExit(harp)
	assertRecaptured(t, canon, "an interactive run's exit")
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
