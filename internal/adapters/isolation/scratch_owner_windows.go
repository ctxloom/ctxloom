package isolation

import (
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// ownedByCurrentUser has no uid to compare against here; the per-user temp
// dir is what keeps another account's scratch out of reach instead.
func ownedByCurrentUser(fs.FileInfo) bool { return true }

// lockOwnerFile blocks until f holds an exclusive LockFileEx over byte 0,
// length 1: the exact range gofrs/flock locks, which is what makes it
// conflict with reapDeadScratch's TryLock. A different range would not.
// Closing f releases it.
func lockOwnerFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}
