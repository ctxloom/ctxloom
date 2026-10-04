//go:build !unix

package filelock

// openGuard is empty where the open flags have no O_NONBLOCK; there the
// regular-file check on the opened handle (requireRegular) is the only guard.
const openGuard = 0
