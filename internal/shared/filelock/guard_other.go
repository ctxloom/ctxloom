//go:build !unix

package filelock

// openGuard is empty where the open flags have no O_NOFOLLOW/O_NONBLOCK; the
// post-open regular-file check (requireRegular) is the only guard there.
const openGuard = 0
