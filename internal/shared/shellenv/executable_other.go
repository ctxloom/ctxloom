//go:build !unix

package shellenv

import "os"

// currentUserCanExecute falls back to mode bits where there is no POSIX
// access check to ask. Windows never reaches here in practice —
// probeLoginShellPath refuses outright, so lookPathIn has no PATH to search —
// but the package must still build for it.
func currentUserCanExecute(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&0o111 != 0
}
