package iox

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// AtomicFile is a STREAMING counterpart to WriteFileAtomic/WriteFileAtomicFs,
// for a writer that produces its bytes incrementally (or hands its path to
// code that writes by path rather than through an io.Writer — see TempPath)
// instead of holding the whole payload in memory up front.
//
// The algorithm is the same one WriteFileAtomicFs runs, split across a
// caller-visible lifecycle: NewAtomicFile creates a unique temp file in the
// destination directory (same "."+base+".*.tmp" pattern, so a concurrent
// writer can never clobber this one's in-flight bytes), Write appends to it,
// and Commit fsyncs, chmods exactly to perm, and renames it over path —
// atomically VISIBLE, not durable by default, exactly like the whole-file
// entry points; pass Durable() to NewAtomicFile to also fsync the parent
// directory on Commit (see Durable's doc). Abort discards the temp file
// without ever touching path.
//
// One AtomicFile is used once. Write after Commit or Abort, and a second
// Commit or Abort, are all refused rather than silently ignored: a caller
// that has lost track of which state it is in is exactly the caller a
// half-written or already-installed file would surprise.
type AtomicFile struct {
	fs      afero.Fs
	path    string
	perm    os.FileMode
	cfg     writeConfig
	tmp     afero.File
	tmpName string
	done    bool
}

// NewAtomicFile creates a unique, empty temp file in path's directory, ready
// for streamed writes via Write or via a path-based writer handed TempPath.
// path's parent directory must already exist — NewAtomicFile does not create
// it, matching WriteFileAtomicFs's precondition. Nothing is visible at path
// until Commit renames the temp file into place.
func NewAtomicFile(path string, perm os.FileMode, opts ...Option) (*AtomicFile, error) {
	fs := afero.NewOsFs()
	dir := filepath.Dir(path)
	tmp, err := afero.TempFile(fs, dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("atomic file %s: create temp file in %s: %w", path, dir, err)
	}
	return &AtomicFile{
		fs:      fs,
		path:    path,
		perm:    perm,
		cfg:     resolveOptions(opts),
		tmp:     tmp,
		tmpName: tmp.Name(),
	}, nil
}

// TempPath returns the private temp file's on-disk path — an escape hatch for
// a caller that must hand a real filesystem path to code that writes by path
// rather than through an io.Writer (e.g. a streaming recorder that opens its
// own append handle). Bytes written there directly are still covered by
// Commit's fsync, chmod, rename, and empty-guard: Commit stats the temp
// file's actual size on disk, not merely what arrived through Write.
//
// Valid only before Commit or Abort runs; the temp file may be gone after
// either.
func (a *AtomicFile) TempPath() string {
	return a.tmpName
}

// Write appends p to the temp file. Refused once Commit or Abort has run —
// see the type doc.
func (a *AtomicFile) Write(p []byte) (int, error) {
	if a.done {
		return 0, fmt.Errorf("atomic file %s: write after commit/abort", a.path)
	}
	return a.tmp.Write(p)
}

// Commit installs the temp file at path: stats it (the empty-guard below),
// fsyncs it, chmods it to perm exactly (not masked by the umask — see
// WriteFileAtomicFs), and renames it over path. If Durable() was passed to
// NewAtomicFile, it then also fsyncs path's parent directory, upgrading the
// rename from atomically visible to visible-plus-durably-named (see
// Durable's doc); a failure of that step is still reported as a Commit
// error, on the same reasoning WriteFileAtomicFs's rename-then-sync applies.
//
// A zero-byte commit over an EXISTING path is refused unless the AllowEmpty
// option was passed to NewAtomicFile — the same guard WriteFileAtomicFs
// applies to a whole-file write, extended here to a streamed one: the size
// checked is whatever actually landed on disk, regardless of whether it
// arrived through Write, through a path-based writer using TempPath, or not
// at all.
//
// Refused a second time, or after Abort. Any failure leaves the AtomicFile
// done (neither Commit nor Abort may be retried) and best-effort removes the
// temp file, mirroring WriteFileAtomicFs's cleanup on every internal failure
// branch.
func (a *AtomicFile) Commit() error {
	if a.done {
		return fmt.Errorf("atomic file %s: commit after commit/abort", a.path)
	}
	a.done = true

	info, err := a.tmp.Stat()
	if err != nil {
		_ = a.tmp.Close()
		_ = a.fs.Remove(a.tmpName)
		return fmt.Errorf("atomic file %s: stat temp file: %w", a.path, err)
	}
	if info.Size() == 0 && !a.cfg.allowEmpty {
		if exists, eerr := afero.Exists(a.fs, a.path); eerr == nil && exists {
			_ = a.tmp.Close()
			_ = a.fs.Remove(a.tmpName)
			return fmt.Errorf("atomic file %s: refusing to commit zero bytes over an existing file", a.path)
		}
	}
	if err := a.tmp.Sync(); err != nil {
		_ = a.tmp.Close()
		_ = a.fs.Remove(a.tmpName)
		return fmt.Errorf("atomic file %s: sync temp file: %w", a.path, err)
	}
	if err := a.tmp.Close(); err != nil {
		_ = a.fs.Remove(a.tmpName)
		return fmt.Errorf("atomic file %s: close temp file: %w", a.path, err)
	}
	if err := a.fs.Chmod(a.tmpName, a.perm); err != nil {
		_ = a.fs.Remove(a.tmpName)
		return fmt.Errorf("atomic file %s: set mode %#o on temp file: %w", a.path, a.perm, err)
	}
	if err := a.fs.Rename(a.tmpName, a.path); err != nil {
		_ = a.fs.Remove(a.tmpName)
		return fmt.Errorf("atomic file %s: rename temp file into place: %w", a.path, err)
	}
	if a.cfg.durable && isOSBackedFs(a.fs) {
		if err := syncDirFn(filepath.Dir(a.path)); err != nil {
			return fmt.Errorf("atomic file %s: sync parent directory: %w", a.path, err)
		}
	}
	return nil
}

// Abort discards the temp file without ever touching path. Best-effort: a
// removal failure is not reported, matching WriteFileAtomicFs's own
// best-effort cleanup on its internal failure branches. Refused a second
// time, or after Commit.
func (a *AtomicFile) Abort() error {
	if a.done {
		return fmt.Errorf("atomic file %s: abort after commit/abort", a.path)
	}
	a.done = true
	_ = a.tmp.Close()
	_ = a.fs.Remove(a.tmpName)
	return nil
}
