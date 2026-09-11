package sessionlock

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// untrustedMagics are the statfs f_type values fsTrusted refuses, by name so
// the reason a sweeper reports is legible. Every entry is a network,
// cluster, or unknowable (FUSE) filesystem; see fsTrusted's doc for the
// denylist rationale.
var untrustedMagics = map[uint32]string{
	unix.NFS_SUPER_MAGIC:   "nfs",
	unix.SMB_SUPER_MAGIC:   "smb",
	unix.SMB2_SUPER_MAGIC:  "smb2",
	unix.CIFS_SUPER_MAGIC:  "cifs",
	unix.AFS_SUPER_MAGIC:   "afs",
	unix.AFS_FS_MAGIC:      "afs",
	unix.CODA_SUPER_MAGIC:  "coda",
	unix.NCP_SUPER_MAGIC:   "ncpfs",
	unix.CEPH_SUPER_MAGIC:  "ceph",
	unix.OCFS2_SUPER_MAGIC: "ocfs2",
	unix.V9FS_MAGIC:        "9p",
	unix.FUSE_SUPER_MAGIC:  "fuse",
	// GFS2 and virtiofs have no constant in x/sys; the values are the
	// kernel's (include/uapi/linux/magic.h). virtiofs is a hypervisor share:
	// the host and the guest are two kernels, so a lock on one side is
	// invisible to the other.
	0x01161970: "gfs2",
	0x6a656a63: "virtiofs",
}

// probeFilesystem identifies dir's filesystem by statfs magic. f_type is a
// signed word whose width varies by architecture; the magic values fit in 32
// bits on every one, so the comparison is made on the truncated unsigned
// value.
func probeFilesystem(dir string) (bool, string, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, "", fmt.Errorf("statfs %s: %w", dir, err)
	}
	magic := uint32(st.Type) //nolint:gosec // f_type's width varies by arch; every magic fits in 32 bits
	if name, denied := untrustedMagics[magic]; denied {
		return false, name, nil
	}
	return true, fmt.Sprintf("0x%x", magic), nil
}
