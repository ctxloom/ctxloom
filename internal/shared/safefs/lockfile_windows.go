//go:build windows

package safefs

import (
	"os"

	"golang.org/x/sys/windows"
)

// openLockFile goes to CreateFile directly because os.OpenFile on Windows
// adds GENERIC_WRITE whenever O_CREATE is set, which would hand back a
// writable handle. The share mode and the read-only attribute for a perm
// without the owner-write bit are os.OpenFile's own.
func openLockFile(path string, perm os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	attrs := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if perm&0o200 == 0 {
		attrs = windows.FILE_ATTRIBUTE_READONLY
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_ALWAYS, attrs, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
