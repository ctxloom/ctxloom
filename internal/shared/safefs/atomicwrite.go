package safefs

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// Option configures a write helper by choosing which decorators it writes
// through. The zero value (no options) is guarded and not durable.
type Option func(*writeConfig)

type writeConfig struct {
	allowEmpty bool
	durable    bool
}

// AllowEmpty strips the empty-write guard (NewGuardFs) for this write. The
// one legitimate shape is a writer that has already decided, with its own
// narrower reasoning, that a zero-byte result is meaningful — a countersign
// index whose last entry was just removed, a gitignore whose only rule was
// retired.
func AllowEmpty() Option {
	return func(c *writeConfig) { c.allowEmpty = true }
}

// Durable writes through NewDurableFs, so the new name survives a power loss
// as well as becoming atomically visible. Costs a directory fsync per write;
// reserve it for a record whose silent reversion after a crash would be worse
// than that cost — a human decision, a rotation lineage — not a cache.
func Durable() Option {
	return func(c *writeConfig) { c.durable = true }
}

func resolveOptions(opts []Option) writeConfig {
	var c writeConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// layers splits fs into where the temp file's bytes go (base: undecorated, so
// they are not synced twice) and what the rename runs through (top: the
// guard and the durability decorator the options chose).
func (c writeConfig) layers(fs afero.Fs) (base, top afero.Fs) {
	base = unguard(fs)
	top = base
	if c.durable {
		top = NewDurableFs(top)
	}
	if !c.allowEmpty {
		top = NewGuardFs(top)
	}
	return base, top
}

// WriteFile writes data to path atomically: a UNIQUE temp file in the same
// directory, fsynced, chmodded, then renamed over path, so a reader sees the
// whole old content or the whole new content and a concurrent writer can
// never clobber this one's in-flight temp. The parent directory must exist.
//
// The rename runs through NewGuardFs unless AllowEmpty() is passed, so zero
// bytes over an existing file fail with ErrEmptyOverwrite and leave it
// intact; zero bytes to a new path proceed. Durable() adds NewDurableFs, so
// the new name is synced too; without it the replacement is atomically
// VISIBLE but the directory entry may revert after a power loss.
//
// perm is applied EXACTLY, via Chmod on the temp file, so the umask does not
// narrow it — unlike afero.WriteFile/os.WriteFile, which pass perm through
// the create call the kernel masks.
func WriteFile(fs afero.Fs, path string, data []byte, perm os.FileMode, opts ...Option) error {
	base, top := resolveOptions(opts).layers(fs)
	dir := filepath.Dir(path)
	tmp, err := afero.TempFile(base, dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		// Every return names path, not the temp name: the temp is an
		// implementation detail the caller never chose, and callers routinely
		// propagate this error unwrapped.
		return fmt.Errorf("atomic write %s: create temp file in %s: %w", path, dir, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = base.Remove(tmp.Name())
		return fmt.Errorf("atomic write %s: write temp file: %w", path, err)
	}
	return install(base, top, tmp, path, perm, "atomic write")
}

// install finishes an atomic write whose bytes are already in tmp: sync,
// close, chmod to perm exactly, rename through top. Any failure removes tmp.
func install(base, top afero.Fs, tmp afero.File, path string, perm os.FileMode, what string) error {
	tmpName := tmp.Name()
	// Without this sync a power loss can persist the rename ahead of the
	// data, leaving an empty or garbage file at path.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = base.Remove(tmpName)
		return fmt.Errorf("%s %s: sync temp file: %w", what, path, err)
	}
	if err := tmp.Close(); err != nil {
		_ = base.Remove(tmpName)
		return fmt.Errorf("%s %s: close temp file: %w", what, path, err)
	}
	if err := base.Chmod(tmpName, perm); err != nil {
		_ = base.Remove(tmpName)
		return fmt.Errorf("%s %s: set mode %#o on temp file: %w", what, path, perm, err)
	}
	if err := Rename(top, tmpName, path); err != nil {
		// A durable-sync failure arrives here after the rename landed; the
		// temp name is already gone then, and this Remove is a no-op.
		_ = base.Remove(tmpName)
		return fmt.Errorf("%s %s: rename temp file into place: %w", what, path, err)
	}
	return nil
}

// WriteFileKeepMode is WriteFile for a file the caller may have already
// authored: an existing file keeps its permission bits, a new one is created
// private (0600). desc names the file in the error the way the caller's user
// knows it ("settings", the basename).
func WriteFileKeepMode(fs afero.Fs, path string, data []byte, desc string, opts ...Option) error {
	perm := os.FileMode(0o600)
	if info, err := fs.Stat(path); err == nil {
		perm = info.Mode().Perm()
	}
	if err := WriteFile(fs, path, data, perm, opts...); err != nil {
		return fmt.Errorf("failed to write %s: %w", desc, err)
	}
	return nil
}
