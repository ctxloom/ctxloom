package iox

import (
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// WriteFileAtomicFs is WriteFileAtomic over an afero filesystem, for writers
// whose filesystem is seam-injected (config). Same contract: a UNIQUE temp
// file in the destination directory, fsynced and then renamed over path, so a
// reader never observes a half-written file — it sees either the whole
// previous content or the whole new content, never a mix. The parent directory
// must already exist.
//
// The guarantee is ATOMIC VISIBILITY, not durability of the replacement. The
// parent directory is not fsynced after the rename, so after a power loss the
// directory entry may still name the previous file; what the temp-file fsync
// buys is that the rename can never become visible ahead of the data behind
// it. A caller that needs the new content to survive a crash must arrange that
// itself.
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
	// Flush the data to stable storage before the rename, mirroring
	// WriteFileAtomic: without it a power loss can persist the rename ahead of
	// the data, leaving an empty or garbage file at path. Sync is a no-op on
	// MemMapFs, so seam-injected tests are unaffected.
	if err := tmp.Sync(); err != nil {
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
