package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedSweepFixture points the project root at a fresh directory and plants one
// ENDED session's engine-home instance in it, credential and all. It returns
// the instance root and the credential path so the caller asserts on BYTES: a
// sweep that reported a removal it never performed is this project's
// characteristic bug, and only the payload catches it.
func seedSweepFixture(t *testing.T) (instanceRoot, credential string) {
	t.Helper()
	testsupport.Isolate(t)
	projectDir := t.TempDir()
	t.Setenv(projectroot.EnvVar, projectDir)
	require.Equal(t, projectDir, projectroot.WorkDir(), "the fixture must own the resolved project root")

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.MarkEnded(entry.HarpName, time.Now()))

	instanceRoot, err = paths.SessionStatePath(filepath.Join(projectDir, paths.AppDirName), entry.HarpName)
	require.NoError(t, err)
	credential = filepath.Join(instanceRoot, paths.SessionHomeDirName, "claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(credential), 0o700))
	require.NoError(t, os.WriteFile(credential, []byte("sk-copied\n"), 0o600))
	require.FileExists(t, credential)
	return instanceRoot, credential
}

// TestSweepOrphanedSessionHomes_ReapsAndReports is the wiring proof: the
// startup helper both entry points call really reaches the reaper, really
// removes the credential, and really says so. A reporting-only assertion would
// pass against a helper that printed a count it never earned.
func TestSweepOrphanedSessionHomes_ReapsAndReports(t *testing.T) {
	instanceRoot, credential := seedSweepFixture(t)

	var buf bytes.Buffer
	SweepOrphanedSessionHomes(&buf)

	assert.NoFileExists(t, credential, "the copied credential must be gone, not merely reported gone")
	assert.NoDirExists(t, instanceRoot)
	assert.Contains(t, buf.String(), "reaped 1 orphaned engine-home instance")
}

// TestSweepOrphanedSessionHomes_SilentWhenNothingToReap: a startup reporter
// that speaks on every launch trains users to ignore it. Nothing to reap, no
// line — the same contract SweepOrphanedWorktrees keeps.
func TestSweepOrphanedSessionHomes_SilentWhenNothingToReap(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(projectroot.EnvVar, t.TempDir())

	var buf bytes.Buffer
	SweepOrphanedSessionHomes(&buf)

	assert.Empty(t, buf.String())
}
