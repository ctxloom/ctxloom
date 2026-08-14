package iox

import (
	"fmt"
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
// The guarantee is ATOMIC VISIBILITY, not durability of the replacement, by
// default: the parent directory is not fsynced after the rename, so after a
// power loss the directory entry may still name the previous file; what the
// temp-file fsync buys is that the rename can never become visible ahead of
// the data behind it. A caller that needs the new content's NAME to survive a
// crash passes Durable() (see its doc), which fsyncs the parent directory too
// — still not full journaling, just the one gap this function otherwise
// leaves open.
//
// Zero-length data is refused when path already exists and neither opts nor
// the caller passed AllowEmpty(): silently succeeding would truncate a live
// file to nothing on what looks like a success path, with the original bytes
// gone. Zero-length data to a path with nothing there yet is not a
// truncation and proceeds — a writer that has never run is indistinguishable
// from one that just produced an empty file, and refusing the second would
// make first-run behavior depend on accidents of timing. AllowEmpty() opts
// out for the rare caller whose own reasoning has already decided an empty
// result is legitimate (see the Option's doc).
func WriteFileAtomicFs(fs afero.Fs, path string, data []byte, perm os.FileMode, opts ...Option) error {
	cfg := resolveOptions(opts)
	if len(data) == 0 && !cfg.allowEmpty {
		if exists, err := afero.Exists(fs, path); err == nil && exists {
			return fmt.Errorf("atomic write %s: refusing to write zero bytes over an existing file", path)
		}
	}
	dir := filepath.Dir(path)
	tmp, err := afero.TempFile(fs, dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		// Every return below names path, not tmpName. The temp name is an
		// implementation detail the caller never chose and cannot act on, and
		// callers routinely propagate this error without wrapping it, so if
		// the target is not named here it is named nowhere.
		return fmt.Errorf("atomic write %s: create temp file in %s: %w", path, dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = fs.Remove(tmpName)
		return fmt.Errorf("atomic write %s: write temp file: %w", path, err)
	}
	// Flush the data to stable storage before the rename, mirroring
	// WriteFileAtomic: without it a power loss can persist the rename ahead of
	// the data, leaving an empty or garbage file at path. Sync is a no-op on
	// MemMapFs, so seam-injected tests are unaffected.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = fs.Remove(tmpName)
		return fmt.Errorf("atomic write %s: sync temp file: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = fs.Remove(tmpName)
		return fmt.Errorf("atomic write %s: close temp file: %w", path, err)
	}
	// perm is applied exactly: Chmod is not masked by the umask, unlike the
	// create call afero.WriteFile/os.WriteFile route perm through. See
	// WriteFileAtomic's doc — this is the contract, not an oversight.
	if err := fs.Chmod(tmpName, perm); err != nil {
		_ = fs.Remove(tmpName)
		return fmt.Errorf("atomic write %s: set mode %#o on temp file: %w", path, perm, err)
	}
	if err := fs.Rename(tmpName, path); err != nil {
		_ = fs.Remove(tmpName)
		return fmt.Errorf("atomic write %s: rename temp file into place: %w", path, err)
	}
	// The rename is already visible at this point; a failure here reports
	// that the NAME is not yet durable, not that the write failed — but it is
	// still returned as an error, because Durable() is a caller asserting it
	// needs that guarantee and a silent downgrade to visibility-only would
	// make the option a no-op nobody could detect.
	if cfg.durable && isOSBackedFs(fs) {
		if err := syncDirFn(dir); err != nil {
			return fmt.Errorf("atomic write %s: sync parent directory %s: %w", path, dir, err)
		}
	}
	return nil
}
