//go:build !windows

package sessionlock

import (
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// inodeOf is the identity of the file the path currently names — the thing a
// rename changes and an in-place write does not.
func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}
	return st.Ino
}

// TestStampPID_KeepsALockedInodeVisiblyAlive is this package's half of the
// inode invariant. safefs proves the primitive keeps the inode
// (TestWriteFileInPlace_KeepsTheSameInode); this proves what that BUYS here —
// that a stamp cannot turn a live session into a reclaimable one.
//
// The mechanism: a probe's verdict is a flock taken on the inode the PATH
// resolves to. A holder's lock lives on the inode it opened. Write the pid by
// renaming a temp file over the path and the two stop being the same inode:
// the holder keeps a lock on a file nothing can reach by name, and the next
// probe opens a fresh, unlocked inode and reports Dead. A sweeper acting on
// that verdict deletes a RUNNING session's data.
//
// The test stamps a file that is currently locked, which Hold itself is
// careful never to do (it stamps BEFORE locking, so no second descriptor is
// ever opened on a locked file — see Hold's doc). That is deliberate: it is
// the sharpest form of the invariant, and it is why this test is
// !windows-only, since LockFileEx genuinely refuses a write through a second
// handle and the question does not arise there.
//
// The control at the end is what keeps the assertion honest: the SAME stamp
// performed with safefs's atomic default must flip the verdict to Dead. Without
// it, a stampPID that had silently gone back to rename-based writing could
// still pass the first half on a filesystem that happened to reuse the inode
// number.
func TestStampPID_KeepsALockedInodeVisiblyAlive(t *testing.T) {
	testsupport.Isolate(t)
	trustEverything(t)

	const harp = "stamped-harp"
	require.NoError(t, Hold(harp))
	t.Cleanup(func() { Release(harp) })

	path := lockPath(t, harp)
	locked := inodeOf(t, path)
	require.Equal(t, Alive, Inspect(harp).Verdict, "the fixture must start from a live session")

	// The migration under test: a re-stamp of the locked file.
	require.NoError(t, stampPID(path))

	assert.Equal(t, locked, inodeOf(t, path),
		"stampPID replaced the inode the lock is held on; a sweeper would now open an unlocked file and read this LIVE session as dead")
	assert.Equal(t, Alive, Inspect(harp).Verdict,
		"the session must still probe as alive after its own pid stamp")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\n", string(raw), "the stamp must still be this process's pid")

	// Control: the same bytes written the way stampPID must NOT write them.
	require.NoError(t, safefs.WriteFile(afero.NewOsFs(), path, raw, lockFileMode))
	require.NotEqual(t, locked, inodeOf(t, path),
		"fixture is not discriminating: the atomic write kept the inode, so the assertion above proves nothing")
	assert.Equal(t, Dead, Inspect(harp).Verdict,
		"fixture is not discriminating: a rename-based stamp must be observable as a live session reading DEAD")
}
