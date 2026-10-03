//go:build windows

package paths

import "golang.org/x/sys/windows"

// documentsDir is the Documents known folder, which a user or policy may
// redirect (OneDrive, a network share), so it is asked for rather than
// assumed under the profile.
func documentsDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Documents, 0)
}
