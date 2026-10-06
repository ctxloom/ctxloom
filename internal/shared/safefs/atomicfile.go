package safefs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
)

// ErrAtomicFileDone: an AtomicFile was written, committed or aborted after it
// had already been committed or aborted.
var ErrAtomicFileDone = errors.New("atomic file already committed or aborted")

// ErrAtomicFileNoPath: Commit was called on a NewAtomicFileIn file, which was
// created without a destination.
var ErrAtomicFileNoPath = errors.New("atomic file has no destination; use CommitAs")

// ErrAtomicFileCrossDir: CommitAs named a destination outside the directory
// the temp file was created in.
var ErrAtomicFileCrossDir = errors.New("atomic file destination is outside the directory its temp file was created in")

// AtomicFile is WriteFile for a writer that produces its bytes incrementally.
// NewAtomicFile creates the unique temp file, Write appends to it, Commit
// installs it exactly as WriteFile does — through the same guard and, with Durable(), the
// same durability decorator — and Abort discards it without touching path.
// NewAtomicFileIn is the same for a writer that learns the destination name
// only after writing (a content-addressed store); it installs with CommitAs.
//
// One AtomicFile is used once: a write after Commit, CommitAs or Abort, and
// a second Commit, CommitAs or Abort, fail with ErrAtomicFileDone rather than
// being ignored.
type AtomicFile struct {
	base, top afero.Fs
	dir       string
	path      string // "" for a NewAtomicFileIn file
	perm      os.FileMode
	tmp       afero.File
	done      bool
}

// NewAtomicFile creates a unique, empty temp file in path's directory, which
// must already exist. Nothing is visible at path until Commit.
func NewAtomicFile(fs afero.Fs, path string, perm os.FileMode, opts ...Option) (*AtomicFile, error) {
	a, err := newAtomicFile(fs, filepath.Dir(path), "."+filepath.Base(path)+".*.tmp", perm, opts)
	if err != nil {
		return nil, fmt.Errorf("atomic file %s: %w", path, err)
	}
	a.path = path
	return a, nil
}

// NewAtomicFileIn creates a unique, empty temp file in dir, which must
// already exist, for a destination named later by CommitAs. Nothing is
// visible under any name until CommitAs.
func NewAtomicFileIn(fs afero.Fs, dir string, perm os.FileMode, opts ...Option) (*AtomicFile, error) {
	a, err := newAtomicFile(fs, filepath.Clean(dir), ".atomic-*.tmp", perm, opts)
	if err != nil {
		return nil, fmt.Errorf("atomic file in %s: %w", dir, err)
	}
	return a, nil
}

// newAtomicFile creates the temp file in dir on the undecorated layer (its
// bytes are synced by install, not twice) and keeps the decorated layer for
// the rename.
func newAtomicFile(fs afero.Fs, dir, pattern string, perm os.FileMode, opts []Option) (*AtomicFile, error) {
	base, top := resolveOptions(opts).layers(fs)
	tmp, err := afero.TempFile(base, dir, pattern)
	if err != nil {
		return nil, fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	return &AtomicFile{base: base, top: top, dir: dir, perm: perm, tmp: tmp}, nil
}

// name is what errors call this file: its destination, or its directory when
// the destination is not chosen yet.
func (a *AtomicFile) name() string {
	if a.path == "" {
		return a.dir
	}
	return a.path
}

// Write appends p to the temp file.
func (a *AtomicFile) Write(p []byte) (int, error) {
	if a.done {
		return 0, fmt.Errorf("atomic file %s: write: %w", a.name(), ErrAtomicFileDone)
	}
	return a.tmp.Write(p)
}

// Commit installs the temp file at the path NewAtomicFile was given:
// CommitAs(path). A NewAtomicFileIn file has no such path, so Commit refuses
// it with ErrAtomicFileNoPath. Any failure leaves the AtomicFile done and
// removes the temp file.
func (a *AtomicFile) Commit() error {
	if !a.done && a.path == "" {
		a.discard()
		return fmt.Errorf("atomic file in %s: commit: %w", a.dir, ErrAtomicFileNoPath)
	}
	return a.CommitAs(a.path)
}

// CommitAs installs the temp file at path exactly as WriteFile would — sync,
// exact chmod, rename through the guard and, with Durable(), the durability
// decorator. path must be in the directory the temp file was created in: a
// rename across directories is not atomic on every filesystem and one
// directory sync would not cover it, so any other directory is refused with
// ErrAtomicFileCrossDir. Any failure leaves the AtomicFile done and removes
// the temp file.
func (a *AtomicFile) CommitAs(path string) error {
	if a.done {
		return fmt.Errorf("atomic file %s: commit: %w", path, ErrAtomicFileDone)
	}
	if filepath.Dir(path) != a.dir {
		a.discard()
		return fmt.Errorf("atomic file %s: commit: %w (%s)", path, ErrAtomicFileCrossDir, a.dir)
	}
	a.done = true
	return install(a.base, a.top, a.tmp, path, a.perm, "atomic file")
}

// Abort discards the temp file without installing it anywhere. Best-effort:
// a removal failure is not reported.
func (a *AtomicFile) Abort() error {
	if a.done {
		return fmt.Errorf("atomic file %s: abort: %w", a.name(), ErrAtomicFileDone)
	}
	a.discard()
	return nil
}

// discard marks a done and removes its temp file, best-effort.
func (a *AtomicFile) discard() {
	a.done = true
	_ = a.tmp.Close()
	_ = a.base.Remove(a.tmp.Name())
}
