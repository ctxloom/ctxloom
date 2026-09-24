//go:build linux

package isolation

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// awaitLockOpens blocks until the lock file directly inside the inotify-watched
// dir has been opened n times. Opening it is how an owner reaches for the lock
// — flock.Lock opens and locks in one call, so this is the only observable
// point between the two, and inotify reports it as an event, not a guess.
func awaitLockOpens(fd, n int) error {
	buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
	for n > 0 {
		r, err := unix.Read(fd, buf)
		if err != nil {
			return err
		}
		for off := 0; off+unix.SizeofInotifyEvent <= r; {
			ev := (*unix.InotifyEvent)(unsafe.Pointer(&buf[off]))
			name := buf[off+unix.SizeofInotifyEvent : off+unix.SizeofInotifyEvent+int(ev.Len)]
			if ev.Mask&unix.IN_OPEN != 0 && string(bytes.TrimRight(name, "\x00")) == ownedScratchLockName {
				n--
			}
			off += unix.SizeofInotifyEvent + int(ev.Len)
		}
	}
	return nil
}

// TestNewOwnedScratch_OwnerOpeningInsideReapersHoldRetries: the owner opens
// its lock file AFTER a reaper has locked the dir but BEFORE the reaper
// deletes it — so the owner holds a handle on a lock file about to be
// unlinked. The reaper must still hold the lock as it deletes, and the owner,
// granted the lock only on the unlinked file, must notice and claim a fresh
// dir; nothing it keeps may vanish, and nothing may be left behind.
//
// Forced, not waited for: scratchCreated runs the reaper up to its delete
// (scratchReapRemove) and parks it there, holding the lock; the owner then
// proceeds, and the delete is released only once inotify reports the owner's
// open of the lock file.
func TestNewOwnedScratch_OwnerOpeningInsideReapersHoldRetries(t *testing.T) {
	parent := t.TempDir()
	const prefix = "ctxloom-holdrace-"

	ino, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(ino) })

	var (
		raced       string
		watchErr    error
		probeLocked bool
		probeErr    error
		awaitErr    error
		inHold      = make(chan struct{})
		ownerOpened = make(chan struct{})
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
		close(inHold)
		<-ownerOpened
		return os.RemoveAll(dir)
	}
	scratchCreated = func(dir string) {
		if raced != "" {
			return
		}
		raced = dir
		if _, watchErr = unix.InotifyAddWatch(ino, dir, unix.IN_OPEN); watchErr != nil {
			close(ownerOpened)
		} else {
			// Opens of the lock file: the reaper's, its probe's, the owner's.
			go func() { awaitErr = awaitLockOpens(ino, 3); close(ownerOpened) }()
		}
		go func() { defer close(reaperDone); reapDeadScratch(parent, prefix) }()
		<-inHold
	}

	s, err := newOwnedScratch(parent, prefix)
	<-reaperDone
	restore()
	require.NoError(t, watchErr)
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
