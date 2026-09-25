package iox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InPlaceMode says what an in-place write does to the destination's EXISTING
// content. It is a required argument with no usable zero value:
// WriteFileInPlace refuses a mode it was not given, so an in-place write is
// always something a call site spelled out, never something a caller fell
// into by leaving a field blank or copying a shorter call.
type InPlaceMode int

const (
	// The zero value is deliberately unnamed and unusable: see InPlaceMode's
	// doc. It is a blank identifier rather than a named constant so nothing
	// can reference it at all.
	_ InPlaceMode = iota
	// TruncateInPlace replaces the file's whole content, truncating whatever
	// was there. The destination is visibly EMPTY to a concurrent reader for
	// the window between the truncate and the write — that exposure is the
	// price of not renaming, and the reason WriteFileAtomic stays the default.
	TruncateInPlace
	// AppendInPlace adds to the end of the file, preserving what is already
	// there. A whole-file replace cannot express this: it would have to read
	// the prior content back and rewrite bytes it did not author.
	AppendInPlace
)

// WriteFileInPlace writes data into the file AT path itself — opening the
// destination and writing through the open descriptor — instead of the
// write-temp-then-rename sequence every other entry point in this package
// performs.
//
// WriteFileAtomic IS THE DEFAULT AND MUST STAY THE DEFAULT. It is the only
// one of the two that gives a reader all-or-nothing content: an in-place
// write is observable half-done, and a TruncateInPlace is observable EMPTY.
// Reach for this function only when the destination CANNOT BE RENAMED OVER,
// and say which reason applies at the call site:
//
//   - The destination is a BIND MOUNT (or any other mount point). A rename
//     onto a bind-mounted path fails with EBUSY — measured on Linux, not
//     inferred — so a rename-based write cannot land on mounted material at
//     all. This is why ctxloom needs an in-place primitive: container runs
//     bind-mount individual files into an engine home, and a write that
//     targets one has no atomic option available to it.
//   - The write must land ON THE SAME INODE some other mechanism is bound
//     to — an flock taken on the path, a reader holding the file open. A
//     rename swaps the inode under all of them.
//   - The prior content must SURVIVE the write (AppendInPlace); a whole-file
//     replace has no way to express an append.
//
// perm is applied EXACTLY, via fchmod on the open descriptor, ONLY when this
// call CREATES the file — matching this package's umask-free contract for a
// file it brought into existence. A destination that already exists KEEPS
// ITS OWN MODE: an in-place write is a write into a file that already
// belongs to someone, and silently re-permissioning it (widening a 0600
// .gitignore to 0644, say) would be a side effect no caller asked for.
//
// A SYMLINKED DESTINATION IS REFUSED. Writing in place means opening a
// destination BY PATH, which is exactly the shape a symlink subverts: a
// repo-tracked `.claude/.credentials.json` pointing at the user's real
// `~/.claude/.credentials.json` turns a routine write into an arbitrary-file
// overwrite. The refusal is made BY THE OPEN SYSCALL (O_NOFOLLOW, plus
// O_EXCL on the create leg, which POSIX already defines as refusing a
// symlink) rather than by an Lstat-then-open check, which is racy: between
// the stat and the open, the path can be replaced with a symlink and the
// check proves nothing. See inplace_unix.go for the flag and
// inplace_windows.go for the one platform where the syscall cannot express
// it.
//
// The file's own bytes are fsynced before Close, and Close's error is
// returned rather than discarded — a discarded Close hides an
// ENOSPC/EDQUOT/EIO write failure behind a nil error, reporting success for
// bytes that never reached disk. Durable() additionally fsyncs the parent
// directory, which only means anything when this call CREATED the file (a
// new directory entry); writing into a file that already existed changes no
// entry, so the unconditional file fsync is the whole guarantee there.
//
// Zero-length data is refused in TruncateInPlace mode when path already
// exists, for the same reason WriteFileAtomicFs refuses it — silently
// emptying a live file on a success path — with the same AllowEmpty()
// escape hatch. AppendInPlace of zero bytes is a no-op, not a truncation,
// and is left alone.
//
// There is no afero twin. In-place writing exists for real mounted
// filesystems, whose defining property (a rename that returns EBUSY, an
// inode other machinery is bound to) no in-memory test double reproduces, so
// a seam-injected variant would only be able to test the parts that were
// never the point.
func WriteFileInPlace(path string, mode InPlaceMode, data []byte, perm os.FileMode, opts ...Option) error {
	cfg := resolveOptions(opts)
	flags, err := inPlaceFlags(path, mode, data, cfg)
	if err != nil {
		return err
	}
	f, created, err := openInPlace(path, flags, perm)
	if err != nil {
		return err
	}
	if err := writeSyncClose(f, path, data, perm, created); err != nil {
		return err
	}
	if cfg.durable {
		dir := filepath.Dir(path)
		if err := syncDirFn(dir); err != nil {
			return fmt.Errorf("in-place write %s: sync parent directory %s: %w", path, dir, err)
		}
	}
	return nil
}

// inPlaceFlags is the open flags for mode, refusing a missing mode and a
// zero-byte truncation over an existing file (unless AllowEmpty).
func inPlaceFlags(path string, mode InPlaceMode, data []byte, cfg writeConfig) (int, error) {
	flags := os.O_WRONLY | openNoFollow
	switch mode {
	case TruncateInPlace:
		if len(data) == 0 && !cfg.allowEmpty {
			if _, err := os.Lstat(path); err == nil {
				return 0, fmt.Errorf("in-place write %s: refusing to write zero bytes over an existing file", path)
			}
		}
		return flags | os.O_TRUNC, nil
	case AppendInPlace:
		return flags | os.O_APPEND, nil
	default:
		return 0, fmt.Errorf("in-place write %s: no InPlaceMode given; pass iox.TruncateInPlace or iox.AppendInPlace, or use WriteFileAtomic, which is the default for any destination that can be renamed over", path)
	}
}

// writeSyncClose writes data through f, sets perm on a file this call
// created, syncs and closes it; f is closed on every path.
//
// Only a file this call created gets its mode set; see WriteFileInPlace's
// doc. fchmod on the descriptor, not Chmod on the path: the path is
// re-resolved by a path-based chmod and would follow a symlink swapped in
// behind us, giving back the traversal the open refused.
func writeSyncClose(f *os.File, path string, data []byte, perm os.FileMode, created bool) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("in-place write %s: write: %w", path, err)
	}
	if created {
		if err := f.Chmod(perm); err != nil {
			_ = f.Close()
			return fmt.Errorf("in-place write %s: set mode %#o: %w", path, perm, err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("in-place write %s: sync: %w", path, err)
	}
	return closeChecked(f, path)
}

// openInPlace opens path for writing without ever traversing a symlink, and
// reports whether this call created the file.
//
// Two legs, not one, because "did we create it?" cannot be answered after the
// fact without a race, and the answer decides whether perm is applied. The
// create leg carries O_EXCL, which POSIX defines as failing on a symlink even
// where O_NOFOLLOW is unavailable; the existing-file leg carries no O_CREATE
// at all, so it can only ever open something that was already there, and its
// O_NOFOLLOW is what refuses a symlink. A symlink planted between the two
// opens is refused by the second leg, not missed.
func openInPlace(path string, flags int, perm os.FileMode) (*os.File, bool, error) {
	f, err := os.OpenFile(path, flags|os.O_CREATE|os.O_EXCL, perm)
	if err == nil {
		return f, true, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, false, wrapOpenInPlace(path, err)
	}
	if err := refuseSymlinkDest(path); err != nil {
		return nil, false, err
	}
	f, err = os.OpenFile(path, flags, perm)
	if err != nil {
		return nil, false, wrapOpenInPlace(path, err)
	}
	return f, false, nil
}

// wrapOpenInPlace names the failure, calling a symlinked destination what it
// is: the kernel reports O_NOFOLLOW-on-a-symlink as ELOOP, which otherwise
// reads as a symlink LOOP and sends the reader hunting for a cycle that does
// not exist.
func wrapOpenInPlace(path string, err error) error {
	if isSymlinkOpenErr(err) {
		return fmt.Errorf("in-place write %s: destination is a symlink; refusing to write through it: %w", path, err)
	}
	return fmt.Errorf("in-place write %s: open: %w", path, err)
}

// closeChecked closes f and, on failure, wraps the error naming path.
//
// Split out so a test can drive Close-error propagation (via an already-closed
// *os.File) without staging a real ENOSPC/EDQUOT/EIO. A deferred, discarded
// Close is the shape this exists to rule out: it reports success for bytes
// that never reached disk, and an in-place APPEND caller that has already
// removed content elsewhere on the strength of that success ends up with
// less than it started with.
func closeChecked(f *os.File, path string) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("in-place write %s: close: %w", path, err)
	}
	return nil
}
