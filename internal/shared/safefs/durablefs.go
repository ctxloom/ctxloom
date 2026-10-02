package safefs

import (
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// durableFs makes the NAMES its writes produce survive a power loss, as an
// afero.Fs decorator. An fsync on a file makes its bytes durable but says
// nothing about the directory entry naming it, so a rename or a create that
// lands just before a crash can come back with the data intact and the old
// name — or no name — in the directory.
//
//   - Rename: after the rename, the destination's parent directory is synced,
//     and the source's too when it differs.
//   - A write-mode file opened through it is synced on Close; if the open
//     created it, its parent directory is synced as well.
//
// Not full journaling: Remove, Mkdir and Chmod are forwarded as they are.
// On Windows a directory cannot be synced at all, so the directory half is a
// no-op there (see dirsync_windows.go).
type durableFs struct {
	afero.Fs
}

// NewDurableFs wraps base so its renames and created files are durably named.
func NewDurableFs(base afero.Fs) afero.Fs {
	if d, ok := base.(*durableFs); ok {
		return d
	}
	return &durableFs{Fs: base}
}

func (d *durableFs) Name() string { return "DurableFs(" + d.Fs.Name() + ")" }

func (d *durableFs) Rename(oldname, newname string) error {
	if err := d.Fs.Rename(oldname, newname); err != nil {
		return err
	}
	newDir, oldDir := filepath.Dir(newname), filepath.Dir(oldname)
	if err := syncDirFn(d.Fs, newDir); err != nil {
		return err
	}
	if oldDir == newDir {
		return nil
	}
	return syncDirFn(d.Fs, oldDir)
}

func (d *durableFs) Create(name string) (afero.File, error) {
	return d.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (d *durableFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		return d.Fs.OpenFile(name, flag, perm)
	}
	created := false
	if flag&os.O_CREATE != 0 {
		_, err := d.Stat(name)
		created = os.IsNotExist(err)
	}
	f, err := d.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &durableFile{File: f, fs: d.Fs, created: created}, nil
}

// durableFile syncs itself before closing, and its parent directory after,
// when the open created it.
type durableFile struct {
	afero.File
	fs      afero.Fs
	created bool
}

func (f *durableFile) Close() error {
	if err := f.Sync(); err != nil {
		_ = f.File.Close()
		return err
	}
	if err := f.File.Close(); err != nil {
		return err
	}
	if !f.created {
		return nil
	}
	return syncDirFn(f.fs, filepath.Dir(f.Name()))
}
