//go:build unix

package containercell

import (
	"fmt"
	"os"
	"syscall"
)

// Owner returns a path's owning uid and gid on the host. It is the observation
// the mode/bytes assertions cannot make: bytes and modes survive a rootful run
// unchanged while the files land owned by root, unreadable and undeletable by
// the user whose tree they are in.
func Owner(path string) (uid, gid int, err error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("no POSIX ownership available for %q on this platform", path)
	}
	return int(st.Uid), int(st.Gid), nil
}
