//go:build linux

package isolation

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// lockOpens consumes the inotify events queued on fd (made IN_NONBLOCK) and
// counts the opens of the lock file directly inside the watched dir. Opening
// it is how an owner reaches for the lock — flock.Lock opens and locks in one
// call, so this is the only observable point between the two. With wait it
// blocks in poll until it has counted at least one.
//
// Counting only works from an empty queue: inotify merges an event into an
// identical one still unread at the queue's tail, so two opens can arrive as
// one.
func lockOpens(fd int, wait bool) (int, error) {
	buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
	n := 0
	for {
		r, err := unix.Read(fd, buf)
		if errors.Is(err, unix.EAGAIN) {
			if !wait || n > 0 {
				return n, nil
			}
			pfd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(pfd, -1); err != nil && !errors.Is(err, unix.EINTR) {
				return n, err
			}
			continue
		}
		if err != nil {
			return n, err
		}
		for off := 0; off+unix.SizeofInotifyEvent <= r; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+int(ev.Len)]
			if ev.Mask&unix.IN_OPEN != 0 && string(bytes.TrimRight(name, "\x00")) == ownedScratchLockName {
				n++
			}
			off += unix.SizeofInotifyEvent + int(ev.Len)
		}
	}
}

// TestNewOwnedScratch_OwnerOpeningInsideReapersHoldRetries: the owner opens
// its lock file AFTER a reaper has locked the dir but BEFORE the reaper
// deletes it — so the owner holds a handle on a lock file about to be
// unlinked. The reaper must still hold the lock as it deletes, and the owner,
// granted the lock only on the unlinked file, must notice and claim a fresh
// dir; nothing it keeps may vanish, and nothing may be left behind.
//
// Forced, not waited for: scratchCreated runs the reaper up to its delete
// (scratchReapRemove) and parks it there, holding the lock, with every
// inotify event so far consumed; the owner then proceeds, and the delete is
// released only once inotify reports the next open of the lock file — the
// owner's.
func TestNewOwnedScratch_OwnerOpeningInsideReapersHoldRetries(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-holdrace-"

	ino, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(ino) })

	var (
		raced       string
		watchErr    error
		probeLocked bool
		probeErr    error
		drainErr    error
		awaitErr    error
		inHold      = make(chan struct{})
		reaperDone  = make(chan struct{})
	)
	origCreated, origRemove := scratchCreated, scratchReapRemove
	restore := func() { scratchCreated, scratchReapRemove = origCreated, origRemove }
	t.Cleanup(restore)

	scratchReapRemove = func(dir string) error {
		// Nobody but the reaper may be able to take the lock here.
		probe := flock.New(filepath.Join(dir, ownedScratchLockName))
		if probeLocked, probeErr = probe.TryLock(); probeLocked {
			_ = probe.Unlock()
		}
		if watchErr == nil {
			_, drainErr = lockOpens(ino, false)
		}
		close(inHold)
		if watchErr == nil && drainErr == nil {
			_, awaitErr = lockOpens(ino, true)
		}
		return os.RemoveAll(dir)
	}
	scratchCreated = func(dir string) {
		if raced != "" {
			return
		}
		raced = dir
		_, watchErr = unix.InotifyAddWatch(ino, dir, unix.IN_OPEN)
		go func() { defer close(reaperDone); reapDeadScratch(parent, prefix) }()
		<-inHold
	}

	s, err := newOwnedScratch(parent, prefix)
	<-reaperDone
	restore()
	require.NoError(t, watchErr)
	require.NoError(t, drainErr)
	require.NoError(t, awaitErr)
	require.NoError(t, err)
	t.Cleanup(s.release)

	require.NoError(t, probeErr)
	assert.False(t, probeLocked, "the reaper holds the owner lock at the moment it deletes")
	assert.NoDirExists(t, raced, "precondition: the reaper took the first dir")
	assert.NotEqual(t, raced, s.dir, "the owner must not keep a dir reaped under it")
	assert.DirExists(t, s.dir)
	assert.FileExists(t, filepath.Join(s.dir, ownedScratchLockName))
	assert.Equal(t, []string{s.dir}, scratchDirs(t, parent, prefix), "nothing left behind")
	reapDeadScratch(parent, prefix)
	assert.DirExists(t, s.dir, "and the owner holds it live against the next reaper")
}
