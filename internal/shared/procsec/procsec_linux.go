//go:build linux

package procsec

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// denySameUIDInspection clears this process's dumpable flag via
// prctl(PR_SET_DUMPABLE, 0).
//
// A non-dumpable process's state-bearing /proc/<pid> entries — environ, mem,
// maps, and the exe/cwd/root magic links — become root-owned, so a same-uid
// peer's read is denied. /proc/<pid>/stat and /proc/<pid>/cmdline stay
// readable, which is what internal/lm/grpc's session sweeper needs to keep
// enumerating members of a runner's session.
//
// The flag is per-process and is RESET TO DUMPABLE ON execve, so it covers
// this process and nothing it spawns.
func denySameUIDInspection() (bool, string, error) {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return false, ReasonMechanismFailed, fmt.Errorf("prctl(PR_SET_DUMPABLE, 0): %w", err)
	}
	return true, ReasonHardened, nil
}
