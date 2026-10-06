package coord

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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
	items, err := openStore(afero.NewOsFs(), filepath.Join(t.TempDir(), "items.jsonl"), itemsF)
	require.NoError(t, err)
	t.Cleanup(func() { _ = items.Close() })
	c := &Coordinator{rep: termRep(), root: safefs.NewMem(mem), stateDir: stateDir, items: items, itemsF: itemsF}

	c.writeItemsSnapshot()

	_, ok := loadItemsSnapshot(termRep(), mem, stateDir)
	assert.True(t, ok, "the snapshot must land in the injected fs")
	_, err = os.Stat(stateDir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}

func TestWriteFinalReport_WritesThroughTheInjectedFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	dir := filepath.Join(absentDiskDir(t), reportsDirName, "some-harp")
	c := &Coordinator{rep: termRep(), root: safefs.NewMem(mem)}

	require.NoError(t, c.writeFinalReport(dir, "some-harp", Summary{Scope: ScopeFinal, Text: "the verdict"}))

	got, err := afero.ReadFile(mem, filepath.Join(dir, finalReportFileName))
	require.NoError(t, err, "the report must land in the injected fs")
	assert.Equal(t, "the verdict", string(got))
	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}

// The journals open, append and replay through the coordinator's fs: a
// journal written to a MemMapFs is replayed from it, and nothing reaches the
// real disk.
func TestOpenStore_JournalsThroughTheInjectedFs(t *testing.T) {
	mem := afero.NewMemMapFs()
	stateDir := absentDiskDir(t)
	path := filepath.Join(stateDir, "items.jsonl")

	first := newItemsFold()
	store, err := openStore(mem, path, first)
	require.NoError(t, err)
	require.NoError(t, store.Exec(func() ([]Fact, error) {
		return []Fact{factAt(factItem, time.Unix(1_700_000_000, 0), itemFact{RunID: "run-a", Seq: 1, Kind: "run_started"})}, nil
	}))
	require.NoError(t, store.Close())

	again := newItemsFold()
	reopened, err := openStore(mem, path, again)
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	assert.Equal(t, 1, again.countsFor("run-a")["run_started"], "the journal replays from the injected fs")
	_, err = os.Stat(stateDir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}

// The owner lock and its stamp live on the coordinator's Root: on an
// in-memory Root a second claimant is refused while the first holds it, the
// probe sees the holder's stamp, and the release frees it — with nothing on
// the real disk.
func TestClaimOwner_OnAMemRoot(t *testing.T) {
	root := safefs.NewMem(afero.NewMemMapFs())
	dir := filepath.Join(absentDiskDir(t), "root-harp")
	require.NoError(t, root.Fs.MkdirAll(dir, safefs.PrivateDirMode))
	stamp := newOwnerStamp("owner-harp", OwnerNonInteractive)

	release, err := claimOwner(root, termRep(), dir, stamp)
	require.NoError(t, err)
	st, err := ProbeOwner(root, dir)
	require.NoError(t, err)
	assert.True(t, st.Held)
	assert.Equal(t, "owner-harp", st.Harp)

	_, err = claimOwner(root, termRep(), dir, newOwnerStamp("rival-harp", OwnerNonInteractive))
	require.ErrorIs(t, err, ErrStateOwned)

	release()
	st, err = ProbeOwner(root, dir)
	require.NoError(t, err)
	assert.False(t, st.Held)
	stamped, _ := afero.Exists(root.Fs, filepath.Join(dir, ownerStampFileName))
	assert.False(t, stamped, "the release unstamps")
	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, fs.ErrNotExist, "nothing may reach the real disk")
}
