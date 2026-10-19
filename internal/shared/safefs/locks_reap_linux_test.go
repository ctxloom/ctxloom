//go:build linux

package safefs

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLock_RetakesTheFreshFileWhenItsFileIsReapedWhileItWaits forces the
// interleaving a reaper makes: a taker opens the lock file and blocks on it,
// and the file is unlinked under its holder — as ReapLockFile unlinks it —
// before the holder lets go. The taker is granted the unlinked file; it must
// let go of that and take the file now at the path, so that what it holds is
// the lock every other taker contends for.
func TestLock_RetakesTheFreshFileWhenItsFileIsReapedWhileItWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	locks := New().Locks
	holder, err := locks.Lock(path)
	require.NoError(t, err)
	reaped := inodeOf(t, path)

	type result struct {
		lk  Lock
		err error
	}
	got := make(chan result, 1)
	go func() {
		lk, err := locks.Lock(path)
		got <- result{lk, err}
	}()
	waitForFlockWaiter(t, reaped)

	require.NoError(t, os.Remove(path))
	require.NoError(t, holder.Unlock())

	var res result
	select {
	case res = <-got:
	case <-time.After(30 * time.Second):
		t.Fatal("the waiter was never granted the lock")
	}
	require.NoError(t, res.err)
	defer func() { _ = res.lk.Unlock() }()
	assert.True(t, res.lk.Current(), "the waiter kept the unlinked file")
	_, err = locks.TryLock(expired(), path)
	require.ErrorIs(t, err, ErrLockHeld, "the waiter's lock does not exclude a taker of the file at the path")
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Sys().(*syscall.Stat_t).Ino
}

// waitForFlockWaiter waits until the kernel lists a flock(2) request BLOCKED
// on inode (a "->" row of /proc/locks): a taker that has opened that file
// and is waiting for it, as opposed to one about to open whatever the path
// names next.
func waitForFlockWaiter(t *testing.T, inode uint64) {
	t.Helper()
	suffix := ":" + strconv.FormatUint(inode, 10)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		f, err := os.Open("/proc/locks")
		require.NoError(t, err)
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 7 || fields[1] != "->" || fields[2] != "FLOCK" {
				continue
			}
			if strings.HasSuffix(fields[6], suffix) {
				_ = f.Close()
				return
			}
		}
		_ = f.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no taker ever blocked on the lock file")
}
