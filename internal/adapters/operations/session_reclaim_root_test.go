package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// srSeedRoot gives an aged, dead session harp a project of its own and the
// coordinator root it founded there, holding a journal: the tree a session
// leaves behind when it exits with runs not ended.
func srSeedRoot(t *testing.T, harp string) (sessionDir, rootDir string) {
	t.Helper()
	sessionDir = srSeedHarp(t, harp)
	projectDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(sessionDir, paths.SessionSidecarFileName), []byte("project_dir: "+projectDir+"\n"), 0o644))
	srBackdate(t, sessionDir)
	srSeedDeadSession(t, harp)

	rootDir, err := coord.RootStateDir("", projectDir, harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(rootDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, coord.OwnerLockFileName), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, "runs.jsonl"), []byte("{}\n"), 0o600))
	return sessionDir, rootDir
}

// TestSweepReclaim_RemovesTheSessionsUnheldCoordinatorRoot: a root nobody
// holds can only ever be adopted by resuming its session, so once that
// session is reaped the root goes with it.
func TestSweepReclaim_RemovesTheSessionsUnheldCoordinatorRoot(t *testing.T) {
	testsupport.Isolate(t)
	sessionDir, rootDir := srSeedRoot(t, "aged-quiet-heron")

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Reclaimed)
	srAssertGone(t, sessionDir, paths.ScratchDirName)
	assert.NoDirExists(t, rootDir, "the reaped session's coordinator root goes with it")
}

// TestSweepReclaim_LeavesARootALiveProcessHolds: a root another live process
// has adopted (a session resumed from this one) is that process's tree; the
// reaped session's own data still goes.
func TestSweepReclaim_LeavesARootALiveProcessHolds(t *testing.T) {
	testsupport.Isolate(t)
	sessionDir, rootDir := srSeedRoot(t, "aged-quiet-heron")
	fl := flock.New(filepath.Join(rootDir, coord.OwnerLockFileName))
	got, err := fl.TryLock()
	require.NoError(t, err)
	require.True(t, got)
	t.Cleanup(func() { _ = fl.Close() })

	res := srReclaim(t, sessions.ReapPolicy{Cutoff: srCutoff(), Apply: true})

	assert.Equal(t, 1, res.Reclaimed, "a held root does not spare the session's own data")
	srAssertGone(t, sessionDir, paths.ScratchDirName)
	assert.FileExists(t, filepath.Join(rootDir, "runs.jsonl"), "a root a live process holds is never removed")
}
