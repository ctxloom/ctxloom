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

// AtomicFile is WriteFile for a writer that produces its bytes incrementally,
// or hands a path to code that writes by path (see TempPath). NewAtomicFile
// creates the unique temp file, Write appends to it, Commit installs it
// exactly as WriteFile does — through the same guard and, with Durable(), the
// same durability decorator — and Abort discards it without touching path.
//
// One AtomicFile is used once: a write after Commit or Abort, and a second
// Commit or Abort, fail with ErrAtomicFileDone rather than being ignored.
type AtomicFile struct {
	base, top afero.Fs
	path      string
	perm      os.FileMode
	tmp       afero.File
	done      bool
}

// NewAtomicFile creates a unique, empty temp file in path's directory, which
// must already exist. Nothing is visible at path until Commit.
func NewAtomicFile(fs afero.Fs, path string, perm os.FileMode, opts ...Option) (*AtomicFile, error) {
	base, top := resolveOptions(opts).layers(fs)
	dir := filepath.Dir(path)
	tmp, err := afero.TempFile(base, dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("atomic file %s: create temp file in %s: %w", path, dir, err)
	}
	return &AtomicFile{base: base, top: top, path: path, perm: perm, tmp: tmp}, nil
}

// TempPath is the temp file's path, for a caller that must hand a path to
// code that writes by path rather than through an io.Writer. Bytes written
// there are still judged by Commit: the guard stats the temp file itself.
// Valid only before Commit or Abort.
func (a *AtomicFile) TempPath() string {
	return a.tmp.Name()
}

// Write appends p to the temp file.
func (a *AtomicFile) Write(p []byte) (int, error) {
	if a.done {
		return 0, fmt.Errorf("atomic file %s: write: %w", a.path, ErrAtomicFileDone)
	}
	return a.tmp.Write(p)
}

// Commit installs the temp file at path. Any failure leaves the AtomicFile
// done and removes the temp file.
func (a *AtomicFile) Commit() error {
	if a.done {
		return fmt.Errorf("atomic file %s: commit: %w", a.path, ErrAtomicFileDone)
	}
	a.done = true
	return install(a.base, a.top, a.tmp, a.path, a.perm, "atomic file")
}

// Abort discards the temp file without touching path. Best-effort: a removal
// failure is not reported.
func (a *AtomicFile) Abort() error {
	if a.done {
		return fmt.Errorf("atomic file %s: abort: %w", a.path, ErrAtomicFileDone)
	}
	a.done = true
	_ = a.tmp.Close()
	_ = a.base.Remove(a.tmp.Name())
	return nil
}
