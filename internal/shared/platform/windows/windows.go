//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	xwin "golang.org/x/sys/windows"
)

// OS is a Windows host's platform behaviour.
type OS struct{}

// DocumentsDir is the Documents known folder, which a user or policy may
// redirect (OneDrive, a network share), so it is asked for rather than
// assumed under the profile.
func (OS) DocumentsDir() (string, error) {
	return xwin.KnownFolderPath(xwin.FOLDERID_Documents, 0)
}

// PrivateTmpfs is absent: Windows offers no per-user memory-backed dir.
func (OS) PrivateTmpfs(func(string) string) (string, bool) { return "", false }

// LinkDir makes link a directory JUNCTION to target. A junction needs no
// privilege, where a symbolic link needs Developer Mode or elevation; the
// price is that it names its target ABSOLUTELY, so it does not resolve once
// the two trees are mounted elsewhere (LinksResolveInContainers).
func (OS) LinkDir(link, target string) error {
	abs, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("junction %s: %w", link, err)
	}
	if err := os.Mkdir(link, 0o700); err != nil {
		return err
	}
	if err := setMountPoint(link, abs); err != nil {
		_ = os.Remove(link)
		return fmt.Errorf("junction %s -> %s: %w", link, abs, err)
	}
	return nil
}

// setMountPoint writes the junction's reparse point onto the empty dir.
func setMountPoint(dir, target string) error {
	p, err := xwin.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	h, err := xwin.CreateFile(p, xwin.GENERIC_WRITE, 0, nil, xwin.OPEN_EXISTING,
		xwin.FILE_FLAG_OPEN_REPARSE_POINT|xwin.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer xwin.CloseHandle(h)
	buf := mountPointReparseData(target)
	var n uint32
	return xwin.DeviceIoControl(h, xwin.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
}

// LinksTo reports whether link is a junction (or any link) resolving to
// target. Nothing at link is an error satisfying fs.ErrNotExist; anything
// else there is false. Windows paths compare case-insensitively.
func (OS) LinksTo(link, target string) (bool, error) {
	if _, err := os.Lstat(link); err != nil {
		return false, err
	}
	got, err := os.Readlink(link)
	if err != nil {
		return false, nil
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return false, fmt.Errorf("junction %s: %w", link, err)
	}
	return strings.EqualFold(filepath.Clean(got), filepath.Clean(abs)), nil
}

// LinksResolveInContainers is false: a junction names an absolute Windows
// path, which means nothing inside a Linux container.
func (OS) LinksResolveInContainers() bool { return false }
