//go:build !unix

package containercell

import (
	"fmt"
	"runtime"
)

// Owner has no POSIX answer off unix. The cell's ownership axis is a POSIX
// claim about a bind mount, so this reports the absence rather than inventing a
// uid — a caller on such a platform must not read 0:0 as "owned by the invoker".
func Owner(path string) (uid, gid int, err error) {
	return 0, 0, fmt.Errorf("POSIX file ownership is not available on %s, so the container cell's ownership axis cannot be observed for %q", runtime.GOOS, path)
}
