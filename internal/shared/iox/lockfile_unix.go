//go:build !windows

package iox

import "os"

func openLockFile(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDONLY, perm)
}
