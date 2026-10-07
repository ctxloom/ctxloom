//go:build !windows

package safefs

import (
	"os"

	"github.com/gofrs/flock"
)

// newKernelLock is gofrs/flock's lock on path: flock(2), or its fcntl
// emulation, over the whole file — which leaves the content readable. create
// opens with lockOpenFlag; otherwise a missing file is not created.
func newKernelLock(path string, create bool) kernelLock {
	if !create {
		return flock.New(path, flock.SetFlag(os.O_RDONLY|lockOpenGuard))
	}
	return flock.New(path, flock.SetPermissions(lockFileMode), flock.SetFlag(lockOpenFlag))
}
