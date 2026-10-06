//go:build !unix

package safefs

// lockOpenGuard is empty where the open flags have no O_NONBLOCK; there the
// regular-file check on the opened handle (requireRegular) is the only guard.
const lockOpenGuard = 0
