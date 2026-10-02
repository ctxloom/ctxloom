// Package iox forwards to safefs and errwriter for the callers that have not
// moved yet, and carries only the names they use. It holds no logic: the
// write library is safefs, the errors-are-values writer is errwriter. Delete
// it when its last importer is gone.
package iox

import (
	"io"
	"os"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/errwriter"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

type (
	Option      = safefs.Option
	InPlaceMode = safefs.InPlaceMode
)

const AppendInPlace = safefs.AppendInPlace

func AllowEmpty() Option { return safefs.AllowEmpty() }
func Durable() Option    { return safefs.Durable() }

func NewErrWriter(w io.Writer) *errwriter.Writer { return errwriter.New(w) }

func WriteFileAtomic(path string, data []byte, perm os.FileMode, opts ...Option) error {
	return safefs.WriteFile(afero.NewOsFs(), path, data, perm, opts...)
}

func WriteFileAtomicFs(fs afero.Fs, path string, data []byte, perm os.FileMode, opts ...Option) error {
	return safefs.WriteFile(fs, path, data, perm, opts...)
}

func AtomicWriteFile(fs afero.Fs, path string, data []byte, desc string, opts ...Option) error {
	return safefs.WriteFileKeepMode(fs, path, data, desc, opts...)
}

func WriteFileInPlace(path string, mode InPlaceMode, data []byte, perm os.FileMode, opts ...Option) error {
	return safefs.WriteFileInPlace(path, mode, data, perm, opts...)
}

func Rename(fs afero.Fs, oldpath, newpath string) error { return safefs.Rename(fs, oldpath, newpath) }

func SyncDir(dir string) error { return safefs.SyncDir(afero.NewOsFs(), dir) }

func SetSyncDirForTesting(fn func(string) error) func() { return safefs.SetSyncDirForTesting(fn) }
