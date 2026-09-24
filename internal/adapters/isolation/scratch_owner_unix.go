//go:build unix

package isolation

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether info names a file this process's user
// owns. A file whose owner cannot be read is treated as not ours.
func ownedByCurrentUser(info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
