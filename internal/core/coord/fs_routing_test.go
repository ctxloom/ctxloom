package coord

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The checkpoint snapshot and the saved FINAL report go through the
// Coordinator's afero.Fs, so a decorator on that fs (the empty-write guard,
// the durability layer) sees them. Each test here hands the subject a MemMapFs rooted at a
// path that exists on no real disk, then proves the bytes landed in the
// injected fs and nowhere else: a raw os call would either fail on the
// missing directory or leave the file on disk.

// absentDiskDir is a path under a real temp dir that is never created, so an
// os.Stat on it tells whether anything reached the real filesystem.
func absentDiskDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "never-on-disk")
}

func TestLoadItemsSnapshot_ReadsThroughTheInjectedFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	stateDir := absentDiskDir(t)
	testsupport.WriteFileString(t, mem, itemsSnapshotPath(stateDir), `{"offset":7}`, 0o600)

	snap, ok := loadItemsSnapshot(termRep(), mem, stateDir)
	require.True(t, ok, "a snapshot present only in the injected fs must load")
	assert.EqualValues(t, 7, snap.Offset)
}

func TestWriteItemsSnapshot_WritesThroughTheInjectedFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	stateDir := absentDiskDir(t)
	require.NoError(t, mem.MkdirAll(stateDir, 0o700))

	itemsF := newItemsFold()
	items, err := openStore(filepath.Join(t.TempDir(), "items.jsonl"), itemsF)
	require.NoError(t, err)
	t.Cleanup(func() { _ = items.Close() })
	c := &Coordinator{rep: termRep(), fs: mem, stateDir: stateDir, items: items, itemsF: itemsF}

	c.writeItemsSnapshot()

	_, ok := loadItemsSnapshot(termRep(), mem, stateDir)
	assert.True(t, ok, "the snapshot must land in the injected fs")
	_, err = os.Stat(stateDir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}

func TestWriteFinalReport_WritesThroughTheInjectedFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	dir := filepath.Join(absentDiskDir(t), reportsDirName, "some-harp")
	c := &Coordinator{rep: termRep(), fs: mem}

	require.NoError(t, c.writeFinalReport(dir, "some-harp", Summary{Scope: ScopeFinal, Text: "the verdict"}))

	got, err := afero.ReadFile(mem, filepath.Join(dir, finalReportFileName))
	require.NoError(t, err, "the report must land in the injected fs")
	assert.Equal(t, "the verdict", string(got))
	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}
