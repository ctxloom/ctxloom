package safefs

import "github.com/spf13/afero"

// Create is fs.Create, for a caller that must hand back an open, truncated
// file rather than write content in one call — an afero.Fs decorator whose
// Create an overlay invokes itself (CopyOnWriteFs's copy-up).
//
// It forwards and deliberately chooses nothing: the flags and the mode are
// whatever fs's own Create gives, and those differ by filesystem (MemMapFs
// leaves a new file with no permission bits, OsFs asks for 0o666 before the
// umask). Rebuilding it on OpenFile with a fixed perm would change the mode of
// every file an overlay copies up through a decorated layer.
func Create(fs afero.Fs, name string) (afero.File, error) {
	return fs.Create(name)
}
