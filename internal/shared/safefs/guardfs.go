package safefs

import (
	"errors"
	"io"
	"os"

	"github.com/spf13/afero"
)

// ErrEmptyOverwrite is the empty-write guard's refusal: zero bytes were about
// to replace a file that already exists. Every writer that produced nothing
// by mistake — an upstream bug, an empty template, a reader that hit EOF early
// — reports success on the same path that silently truncates a live file, and
// no library ships a check for it, so this one is ours.
var ErrEmptyOverwrite = errors.New("refusing to write zero bytes over an existing file")

// guardFs is the empty-write guard as an afero.Fs decorator. It judges the two
// shapes through which an fs can replace a file's content with nothing:
//
//   - Rename of a zero-length regular file over an existing path — the last
//     step of every atomic write.
//   - An open that truncates (O_TRUNC, which Create implies) an existing
//     regular file. The truncation is DEFERRED until the first non-empty write
//     or an explicit Truncate; a Close with neither leaves the old bytes intact
//     and returns ErrEmptyOverwrite. Deferral rather than a refusal at open is
//     what lets a streaming writer that does produce bytes proceed unchanged.
//
// It is advice around the fs, not a lock: a concurrent writer can still race
// the Stat it takes. Serialization is the file lock's job.
type guardFs struct {
	afero.Fs
}

// NewGuardFs wraps base in the empty-write guard. Wrapping an fs that is
// already guarded returns it unchanged, so callers can guard defensively.
func NewGuardFs(base afero.Fs) afero.Fs {
	if g, ok := base.(*guardFs); ok {
		return g
	}
	return &guardFs{Fs: base}
}

// unguard is base with a top-level guard removed: the AllowEmpty escape.
func unguard(fs afero.Fs) afero.Fs {
	if g, ok := fs.(*guardFs); ok {
		return g.Fs
	}
	return fs
}

func (g *guardFs) Name() string { return "GuardFs(" + g.Fs.Name() + ")" }

// isRegular reports whether path names an existing regular file.
func (g *guardFs) isRegular(path string) (os.FileInfo, bool) {
	info, err := g.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	return info, true
}

func (g *guardFs) Rename(oldname, newname string) error {
	if src, ok := g.isRegular(oldname); ok && src.Size() == 0 {
		if _, err := g.Stat(newname); err == nil {
			return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: ErrEmptyOverwrite}
		}
	}
	return g.Fs.Rename(oldname, newname)
}

// Create forwards to base.Create for a new path, so the mode stays whatever
// the base's own Create gives (see Create in create.go).
func (g *guardFs) Create(name string) (afero.File, error) {
	if _, ok := g.isRegular(name); !ok {
		return g.Fs.Create(name)
	}
	return g.openPending(name, os.O_RDWR, 0)
}

func (g *guardFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	writes := flag&(os.O_WRONLY|os.O_RDWR) != 0
	if flag&os.O_TRUNC == 0 || !writes {
		return g.Fs.OpenFile(name, flag, perm)
	}
	if _, ok := g.isRegular(name); !ok {
		return g.Fs.OpenFile(name, flag, perm)
	}
	return g.openPending(name, flag&^os.O_TRUNC, perm)
}

func (g *guardFs) openPending(name string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := g.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &pendingTruncFile{File: f, pending: true}, nil
}

// pendingTruncFile is an open file whose requested truncation has not
// happened yet. Until it does, it presents as the empty file the caller asked
// for.
type pendingTruncFile struct {
	afero.File
	pending bool
}

// arm performs the deferred truncation; a write is about to land.
func (p *pendingTruncFile) arm() error {
	if !p.pending {
		return nil
	}
	p.pending = false
	return p.File.Truncate(0)
}

func (p *pendingTruncFile) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if err := p.arm(); err != nil {
		return 0, err
	}
	return p.File.Write(b)
}

func (p *pendingTruncFile) WriteAt(b []byte, off int64) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if err := p.arm(); err != nil {
		return 0, err
	}
	return p.File.WriteAt(b, off)
}

func (p *pendingTruncFile) WriteString(s string) (int, error) {
	return p.Write([]byte(s))
}

func (p *pendingTruncFile) Truncate(size int64) error {
	p.pending = false
	return p.File.Truncate(size)
}

func (p *pendingTruncFile) Read(b []byte) (int, error) {
	if p.pending {
		return 0, io.EOF
	}
	return p.File.Read(b)
}

func (p *pendingTruncFile) ReadAt(b []byte, off int64) (int, error) {
	if p.pending {
		return 0, io.EOF
	}
	return p.File.ReadAt(b, off)
}

// Seek relative to the end measures from the empty file the caller asked for.
func (p *pendingTruncFile) Seek(offset int64, whence int) (int64, error) {
	if p.pending && whence == io.SeekEnd {
		whence = io.SeekStart
	}
	return p.File.Seek(offset, whence)
}

func (p *pendingTruncFile) Stat() (os.FileInfo, error) {
	info, err := p.File.Stat()
	if err != nil || !p.pending {
		return info, err
	}
	return emptyInfo{info}, nil
}

func (p *pendingTruncFile) Close() error {
	err := p.File.Close()
	if p.pending {
		return &os.PathError{Op: "close", Path: p.Name(), Err: ErrEmptyOverwrite}
	}
	return err
}

// emptyInfo is a FileInfo reporting size zero: the pending file's view.
type emptyInfo struct{ os.FileInfo }

func (emptyInfo) Size() int64 { return 0 }
