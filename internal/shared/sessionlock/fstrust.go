package sessionlock

// fsTrusted reports whether a lock taken on a file under dir can be trusted
// to prove its holder dead, and names the filesystem type it found.
//
// The answer is a DENYLIST of the filesystems where flock is known not to
// mean what this package needs, not an allowlist of the ones where it does:
// a local filesystem the list has never heard of goes through the kernel's
// generic VFS locking like every other, and refusing it would silently turn
// every sweep on that machine into a no-op. What is denied:
//
//   - network filesystems (NFS, SMB/CIFS, AFS, Coda, Ceph, NCP, 9p, the
//     cluster filesystems): locks there are historically unreliable, and
//     the failure direction is the dangerous one — a lock that does not lock
//     lets a sweep reclaim a LIVE session's data;
//   - FUSE: the kernel cannot say whether the mount is local (an encrypted
//     home) or a network share (sshfs, rclone), and "cannot determine" is a
//     refusal, never a permission;
//   - a platform this package has no probe for (see fstrust_other.go).
//
// An error means the directory could not be identified; callers treat that
// as untrusted too.
func fsTrusted(dir string) (trusted bool, fstype string, err error) {
	return probeFilesystem(dir)
}
