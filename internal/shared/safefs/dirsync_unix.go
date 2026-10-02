//go:build !windows

package safefs

import "github.com/spf13/afero"

// syncDir opens dir through fs and fsyncs it.
func syncDir(fs afero.Fs, dir string) error {
	d, err := fs.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}
