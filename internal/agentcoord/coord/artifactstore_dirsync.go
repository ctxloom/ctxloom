//go:build !windows

package coord

import "os"

// fsyncDir makes a just-created directory entry durable by fsyncing the
// directory itself. Renaming a temp file into place makes the blob VISIBLE;
// only this makes the name survive a crash.
//
// Unix-only: on Windows a directory handle cannot be fsynced, and the
// windows build supplies a documented no-op instead.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
