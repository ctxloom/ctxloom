//go:build darwin || freebsd

package sessionlock

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// untrustedTypeNames are the statfs f_fstypename values fsTrusted refuses on
// the BSD-derived platforms (macOS, FreeBSD): the network filesystems, and
// anything FUSE-backed, which the kernel cannot tell apart from a network
// share. See fsTrusted's doc for the denylist rationale.
var untrustedTypeNames = map[string]bool{
	"nfs":    true,
	"smbfs":  true,
	"afpfs":  true,
	"webdav": true,
	"cifs":   true,
	"acfs":   true,
}

// probeFilesystem identifies dir's filesystem by its statfs type name.
func probeFilesystem(dir string) (bool, string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, "", fmt.Errorf("statfs %s: %w", dir, err)
	}
	name := strings.TrimRight(string(st.Fstypename[:]), "\x00")
	if untrustedTypeNames[name] || strings.Contains(name, "fuse") {
		return false, name, nil
	}
	return true, name, nil
}
