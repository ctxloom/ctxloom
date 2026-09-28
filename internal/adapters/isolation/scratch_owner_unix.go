//go:build unix

package isolation

import (
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// ownedByCurrentUser reports whether info names a file this process's user
// owns. A file whose owner cannot be read is treated as not ours.
func ownedByCurrentUser(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// lockOwnerFile blocks until f holds an exclusive flock(2): the call
// gofrs/flock's Lock makes, so it conflicts with reapDeadScratch's TryLock.
// Closing f (its last descriptor) releases it.
func lockOwnerFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
