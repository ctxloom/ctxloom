package spool

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// The tests in this file pin that every exported spool entry point reaches
// the filesystem ONLY through the afero.Fs it is handed. Each one works
// against a MemMapFs whose files do not exist on disk: an entry point that
// still touched the OS directly would not see them, or would write beside
// them.

// memSpool returns an in-memory filesystem and the home mapper over a HOME of
// the test's own.
func memSpool(t *testing.T) (afero.Fs, PathMapper) {
	t.Helper()
	hostHome(t)
	return afero.NewMemMapFs(), NewHomeMapper()
}

// seedMem publishes one message into dir through fs, and proves it is NOT on
// the disk, so a reader that finds it can only have looked through fs.
func seedMem(t *testing.T, fs afero.Fs, m PathMapper, dir Dir, body string) Ref {
	t.Helper()
	w, err := NewWriter(fs, m, testHarp, dir, "coord")
	require.NoError(t, err)
	ref, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, Body: body})
	require.NoError(t, err)
	requireNotOnDisk(t, m, ref)
	return ref
}

// requireNotOnDisk asserts ref's file is absent from the OS filesystem.
func requireNotOnDisk(t *testing.T, m PathMapper, ref Ref) {
	t.Helper()
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "%s must exist only in the injected fs, got stat err %v", path, err)
}

func TestSweepAndRead_ReachTheDiskOnlyThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	ref := seedMem(t, fs, m, DirIn, "only in memory\n")

	res, err := Sweep(fs, m, testHarp, DirIn)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	require.Len(t, res.Entries, 1, "Sweep must list the in-memory file")
	require.Equal(t, ref, res.Entries[0].Ref)

	msg, err := Read(fs, m, ref)
	require.NoError(t, err)
	require.Equal(t, "only in memory\n", msg.Body)
}

// requireInFs asserts ref's file exists in fs.
func requireInFs(t *testing.T, fs afero.Fs, m PathMapper, ref Ref) {
	t.Helper()
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	ok, err := afero.Exists(fs, path)
	require.NoError(t, err)
	require.True(t, ok, "%s must exist in the injected fs", path)
}

func TestConsume_MovesStampsAndPrunesThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	now := time.Now()
	stale := seedMem(t, fs, m, DirOut, "routed long ago\n")
	staleMoved, err := Consume(fs, m, stale, now.Add(-2*DeliveredRetention))
	require.NoError(t, err)
	requireInFs(t, fs, m, staleMoved)

	ref := seedMem(t, fs, m, DirOut, "routed now\n")
	moved, err := Consume(fs, m, ref, now)
	require.NoError(t, err)
	requireInFs(t, fs, m, moved)
	requireNotOnDisk(t, m, moved)
	path, err := m.Resolve(moved)
	require.NoError(t, err)
	info, err := fs.Stat(path)
	require.NoError(t, err)
	require.WithinDuration(t, now, info.ModTime(), time.Second, "the route time is stamped through fs")
	stalePath, err := m.Resolve(staleMoved)
	require.NoError(t, err)
	gone, err := afero.Exists(fs, stalePath)
	require.NoError(t, err)
	require.False(t, gone, "a copy routed before the retention window is pruned through fs")
}

func TestWithdrawAndFail_MoveThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	steer := seedMem(t, fs, m, DirIn, "retract me\n")
	withdrawn, err := Withdraw(fs, m, steer)
	require.NoError(t, err)
	requireInFs(t, fs, m, withdrawn)
	requireNotOnDisk(t, m, withdrawn)

	bad := seedMem(t, fs, m, DirIn, "refuse me\n")
	require.NoError(t, Fail(fs, m, bad))
	failedDir, err := DirPath(m, testHarp, FailedDirName)
	require.NoError(t, err)
	ok, err := afero.Exists(fs, filepath.Join(failedDir, bad.Name))
	require.NoError(t, err)
	require.True(t, ok, "Fail must move the file within the injected fs")
}

func TestDeliver_RecordsAndDeletesThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	ref := seedMem(t, fs, m, DirIn, "deliver me\n")
	id := ref.Name[:len(ref.Name)-len(".md")]

	require.NoError(t, Deliver(fs, m, ref, id, time.Now()))
	path, err := m.Resolve(ref)
	require.NoError(t, err)
	there, err := afero.Exists(fs, path)
	require.NoError(t, err)
	require.False(t, there, "Deliver must delete the delivered file from the injected fs")

	delivered, err := Delivered(fs, m, testHarp, id)
	require.NoError(t, err)
	require.True(t, delivered, "the record must be read back through fs")
	ids, err := DeliveredIdentities(fs, m, testHarp)
	require.NoError(t, err)
	require.Contains(t, ids, id)
	_, statErr := os.Stat(deliveredPath(t, m, id))
	require.True(t, os.IsNotExist(statErr), "the record must not be written to disk")
}

func TestClaimAndPending_ReachTheSpoolThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	w, err := NewWriter(fs, m, testHarp, DirIn, "coord")
	require.NoError(t, err)
	for _, body := range []string{"a\n", "b\n"} {
		_, err := w.Write(&Message{Kind: "message", FromHarp: "coord", To: testHarp, OriginID: "mail-mem", Body: body})
		require.NoError(t, err)
	}

	pending, err := Pending(fs, m, testHarp)
	require.NoError(t, err)
	require.True(t, pending, "Pending must see the in-memory in/")

	res, err := Claim(fs, m, testHarp)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	require.Len(t, res.Entries, 1, "the in-flight twin is discarded, through fs")
	require.Equal(t, ClaimedDirName, res.Entries[0].Ref.Dir)
	claimedDir, err := DirPath(m, testHarp, ClaimedDirName)
	require.NoError(t, err)
	ok, err := afero.Exists(fs, filepath.Join(claimedDir, res.Entries[0].Ref.Name))
	require.NoError(t, err)
	require.True(t, ok, "the claim is a rename inside the injected fs")
	_, statErr := os.Stat(claimedDir)
	require.True(t, os.IsNotExist(statErr), "nothing is claimed on disk")

	pending, err = Pending(fs, m, testHarp)
	require.NoError(t, err)
	require.False(t, pending, "everything in the in-memory in/ was claimed or discarded")
}

func TestWakes_ArmListConsumeAndClearThroughFs(t *testing.T) {
	fs, m := memSpool(t)
	nonce, err := ArmWake(fs, m, testHarp)
	require.NoError(t, err)
	dir, err := wakeDir(m, testHarp)
	require.NoError(t, err)
	ok, err := afero.Exists(fs, filepath.Join(dir, nonce))
	require.NoError(t, err)
	require.True(t, ok, "the nonce is armed in the injected fs")
	_, statErr := os.Stat(dir)
	require.True(t, os.IsNotExist(statErr), "no wake is armed on disk")

	out, err := OutstandingWake(fs, m, testHarp)
	require.NoError(t, err)
	require.Equal(t, []string{nonce}, out)
	gone, err := ConsumeWake(fs, m, testHarp, nonce)
	require.NoError(t, err)
	require.True(t, gone, "the in-memory nonce is redeemed")

	_, err = ArmWake(fs, m, testHarp)
	require.NoError(t, err)
	cleared, err := ClearWakes(fs, m, testHarp)
	require.NoError(t, err)
	require.Equal(t, 1, cleared)
}
