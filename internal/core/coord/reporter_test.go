package coord

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// Two coordinators in one process each report to the Reporter they were
// constructed with — never to a process-wide channel. An unreadable items
// snapshot (a directory where the file belongs) is the deterministic
// trigger: New reads it synchronously, and the warning names the path it
// could not read, so cross-talk is visible as the other coordinator's state
// dir in the wrong collector.
func TestNew_TwoCoordinatorsInOneProcess_ReportToTheirOwnReporters(t *testing.T) {
	teeHome(t)
	unreadableSnapshot := func(t *testing.T) string {
		dir := t.TempDir()
		require.NoError(t, os.MkdirAll(itemsSnapshotPath(dir), 0o700))
		return dir
	}
	dirA, dirB := unreadableSnapshot(t), unreadableSnapshot(t)

	var foundA, foundB report.Collector
	a, err := New(Options{ProjectDir: t.TempDir(), StateDir: dirA, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp, Reporter: &foundA})
	require.NoError(t, err)
	t.Cleanup(a.Close)
	b, err := New(Options{ProjectDir: t.TempDir(), StateDir: dirB, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp, Reporter: &foundB})
	require.NoError(t, err)
	t.Cleanup(b.Close)

	require.Len(t, foundA.All(), 1, "A's unreadable snapshot reaches A's reporter, once")
	require.Len(t, foundB.All(), 1, "B's unreadable snapshot reaches B's reporter, once")
	assert.Contains(t, foundA.All()[0].Text, dirA)
	assert.NotContains(t, foundA.All()[0].Text, dirB, "B's warning must not reach A's reporter")
	assert.Contains(t, foundB.All()[0].Text, dirB)
	assert.NotContains(t, foundB.All()[0].Text, dirA, "A's warning must not reach B's reporter")
	assert.False(t, foundA.All()[0].Fatal(), "a snapshot fallback is a warning, not a fail-loudly finding")
}

// A nil Reporter is the same silence as before the coordinator had one — the
// composition root chooses the sink; a test that passes none gets no panic.
func TestNew_NilReporter_IsSilent(t *testing.T) {
	teeHome(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, itemsSnapshotFileName), []byte("{not json"), 0o600))
	c, err := New(Options{ProjectDir: t.TempDir(), StateDir: dir, Spawner: newFakeSpawner(nil, nil), OwnerHarp: ownerIdentity().Harp})
	require.NoError(t, err)
	c.Close()
}
