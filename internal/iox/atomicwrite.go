package iox

import (
	"os"
	"path/filepath"
)

// WriteFileAtomic writes data to path by creating a UNIQUE temp file in the same
// directory, then renaming it over path. The unique name (not a fixed
// "<path>.tmp") keeps a concurrent writer — even one that slipped past an advisory
// lock — from clobbering another writer's in-flight temp before the rename, so a
// reader never observes a half-written file or a truncated peer temp. The parent
// directory must already exist. perm is applied to the final file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
