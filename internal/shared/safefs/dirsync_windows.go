//go:build windows

package safefs

import "github.com/spf13/afero"

// syncDir is a no-op on Windows: a directory handle cannot be flushed there,
// so the directory half of durability is unix-only rather than claimed
// everywhere.
func syncDir(afero.Fs, string) error { return nil }
