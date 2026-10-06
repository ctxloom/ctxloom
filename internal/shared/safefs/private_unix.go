//go:build !windows

package safefs

import (
	"os"

	"github.com/spf13/afero"
)

// osPrivate is owner-only on unix: the mode bits, restricted by chmod.
func osPrivate() privateOn {
	return privateOn{fs: afero.NewOsFs(), violation: modeViolation, restrict: func(dir string) error {
		return os.Chmod(dir, PrivateDirMode)
	}}
}
