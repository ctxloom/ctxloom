//go:build !windows

package sessionlock

import (
	"os"

	"github.com/gofrs/flock"
)

// newHarpLock is gofrs/flock's advisory lock: flock(2) (or its fcntl
// emulation) locks the whole file without making its content unreadable, so
// the pid stays readable to anyone while the holder lives.
func newHarpLock(path string, flag int) harpLock {
	return flock.New(path, flock.SetFlag(flag), flock.SetPermissions(lockFileMode))
}

// harpLockFlagCreate and harpLockFlagExisting are the two ways a harp lock
// is opened: Hold creates the file, a probe must never.
const (
	harpLockFlagCreate   = os.O_CREATE | os.O_RDONLY
	harpLockFlagExisting = os.O_RDONLY
)
