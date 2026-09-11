package sessionlock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// probeFilesystem identifies dir's volume by GetDriveType on its root: a UNC
// path is a network share by construction, and DRIVE_REMOTE is one by
// mount. Anything the API cannot classify is an error, so it is refused
// upstream rather than assumed local.
func probeFilesystem(dir string) (bool, string, error) {
	if _, err := os.Stat(dir); err != nil {
		return false, "", err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, "", err
	}
	if strings.HasPrefix(abs, `\\`) {
		return false, "unc", nil
	}
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(abs) + `\`)
	if err != nil {
		return false, "", err
	}
	switch dt := windows.GetDriveType(root); dt {
	case windows.DRIVE_REMOTE:
		return false, "remote", nil
	case windows.DRIVE_UNKNOWN, windows.DRIVE_NO_ROOT_DIR:
		return false, "", fmt.Errorf("GetDriveType(%s): unclassifiable volume (%d)", abs, dt)
	default:
		return true, fmt.Sprintf("drive-type-%d", dt), nil
	}
}
