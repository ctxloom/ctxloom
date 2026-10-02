//go:build !windows

package safefs

import "github.com/spf13/afero"

// Rename is fs.Rename: replacing a path another process has open is an
// ordinary rename(2) here. See the Windows twin for why the seam exists.
func Rename(fs afero.Fs, oldpath, newpath string) error {
	return fs.Rename(oldpath, newpath)
}
