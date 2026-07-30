//go:build unix

package shellenv

import "golang.org/x/sys/unix"

// currentUserCanExecute reports whether THIS process may exec path, using the
// kernel's own effective-uid check rather than reading mode bits.
//
// The two are not the same question. Mode bits answer "does somebody have an
// execute bit here?"; a file at 0o011 says yes while POSIX refuses the owner,
// because the owner bits alone are consulted once euid matches. exec.LookPath's
// Unix implementation asks the kernel for exactly this reason, and lookPathIn
// must agree with it or it will hand back a path the subsequent exec rejects.
//
// AT_EACCESS selects EFFECTIVE ids: the identity the exec will actually run
// under. Any error — EACCES, ENOENT, a filesystem that cannot answer — is a
// no, which is the fail-closed direction: the caller keeps searching the rest
// of the PATH and ultimately reports exec.LookPath's own error.
func currentUserCanExecute(path string) bool {
	return unix.Faccessat(unix.AT_FDCWD, path, unix.X_OK, unix.AT_EACCESS) == nil
}
