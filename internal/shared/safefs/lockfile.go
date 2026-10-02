package safefs

import "os"

// OpenLockFile opens the advisory-lock file at path, creating it empty with
// perm if it is absent, and returns a read-only handle to take a lock on. It
// never writes or truncates: a lock file's content is irrelevant, only the
// open handle is.
//
// The flags are github.com/gofrs/flock's own (O_CREATE|O_RDONLY), so a lock
// taken on this handle contends with a flock.Flock on the same path. Opening
// is kept apart from locking because a caller may need to act between the
// two, which flock's single open-and-lock call cannot offer.
//
// A missing parent directory surfaces as fs.ErrNotExist on every platform.
func OpenLockFile(path string, perm os.FileMode) (*os.File, error) {
	return openLockFile(path, perm)
}
