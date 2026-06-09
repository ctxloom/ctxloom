package iox

import (
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// WriteFileAtomicFs is WriteFileAtomic over an afero filesystem, for writers
// whose filesystem is seam-injected (config). Same contract: a UNIQUE temp
// file in the destination directory, renamed over path, so a reader never
// observes a half-written file. The parent directory must already exist.
func WriteFileAtomicFs(fs afero.Fs, path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := afero.TempFile(fs, dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = fs.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = fs.Remove(tmpName)
		return err
	}
	if err := fs.Chmod(tmpName, perm); err != nil {
		_ = fs.Remove(tmpName)
		return err
	}
	if err := fs.Rename(tmpName, path); err != nil {
		_ = fs.Remove(tmpName)
		return err
	}
	return nil
}
