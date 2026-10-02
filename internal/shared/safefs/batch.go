package safefs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"
)

// Edit folds one change into a file's STAGED content: cur and exists are what
// the previous edit left (the file as read, for the first), and keep=false
// means the file should not exist.
type Edit func(cur []byte, exists bool) (next []byte, keep bool, err error)

// Seal runs under the path's lock once every edit has folded and before the
// write, with the file as read and as it is about to be. It is where a record
// of the change is made durable BEFORE its target changes, so a crash between
// the two leaves a record that knows the write may not have happened.
type Seal func(before []byte, existed bool, after []byte, keep bool) error

// Locker serializes one path's commit. It is injected because the lock-file
// scheme is not this package's: safefs is shared and cannot know where a
// path's lock lives.
type Locker func(path string, fn func() error) error

// Batch accumulates edits to any number of files and writes each changed file
// ONCE, on Commit. A file touched by several writers in one operation is read
// once and written once, however many edits it takes.
type Batch struct {
	fs    afero.Fs
	lock  Locker
	opts  []Option
	files map[string]*staged
}

type staged struct {
	edits []Edit
	seals []Seal
}

// Committed is what a Commit did, each list sorted.
type Committed struct{ Written, Removed, Unchanged []string }

// NewBatch is an empty batch writing through fs, locking with lock. opts are
// the write options every file's write takes (AllowEmpty, Durable).
func NewBatch(fs afero.Fs, lock Locker, opts ...Option) *Batch {
	return &Batch{fs: fs, lock: lock, opts: opts, files: map[string]*staged{}}
}

func (b *Batch) file(path string) *staged {
	s, ok := b.files[path]
	if !ok {
		s = &staged{}
		b.files[path] = s
	}
	return s
}

// Edit stages e on path, after every edit already staged there.
func (b *Batch) Edit(path string, e Edit) { b.file(path).edits = append(b.file(path).edits, e) }

// Seal stages s on path; seals run in the order staged.
func (b *Batch) Seal(path string, s Seal) { b.file(path).seals = append(b.file(path).seals, s) }

// fold is one path's read and folded result, held until every path folds.
type fold struct {
	path          string
	before, after []byte
	existed, keep bool
	seals         []Seal
}

func (f fold) changed() bool {
	return f.existed != f.keep || !bytes.Equal(f.before, f.after)
}

// Commit takes every path's lock in sorted order — one order for every batch,
// so two batches cannot deadlock — then reads and folds every path, and only
// when every edit has succeeded seals and writes each changed path once. A
// failed edit therefore writes nothing at all. A failed seal or write stops
// the commit there: the paths before it are written, which is the residual
// of having no cross-file atomicity.
func (b *Batch) Commit() (Committed, error) {
	paths := make([]string, 0, len(b.files))
	for p := range b.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out Committed
	err := b.locked(paths, func() error {
		folds := make([]fold, 0, len(paths))
		for _, p := range paths {
			f, err := b.fold(p)
			if err != nil {
				return err
			}
			folds = append(folds, f)
		}
		for _, f := range folds {
			if err := b.land(f, &out); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// locked runs fn holding the lock of every path, nested in the order given.
func (b *Batch) locked(paths []string, fn func() error) error {
	if len(paths) == 0 {
		return fn()
	}
	return b.lock(paths[0], func() error { return b.locked(paths[1:], fn) })
}

func (b *Batch) fold(path string) (fold, error) {
	before, err := afero.ReadFile(b.fs, path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return fold{}, fmt.Errorf("read %s: %w", path, err)
	}
	s := b.files[path]
	cur, keep := before, existed
	for _, e := range s.edits {
		if cur, keep, err = e(cur, keep); err != nil {
			return fold{}, err
		}
		if !keep {
			cur = nil
		}
	}
	return fold{path: path, before: before, existed: existed, after: cur, keep: keep, seals: s.seals}, nil
}

func (b *Batch) land(f fold, out *Committed) error {
	if !f.changed() {
		out.Unchanged = append(out.Unchanged, f.path)
		return nil
	}
	for _, s := range f.seals {
		if err := s(f.before, f.existed, f.after, f.keep); err != nil {
			return err
		}
	}
	if !f.keep {
		if err := b.fs.Remove(f.path); err != nil {
			return fmt.Errorf("remove %s: %w", f.path, err)
		}
		if resolveOptions(b.opts).durable {
			if err := syncDirFn(b.fs, filepath.Dir(f.path)); err != nil {
				return fmt.Errorf("remove %s: sync directory: %w", f.path, err)
			}
		}
		out.Removed = append(out.Removed, f.path)
		return nil
	}
	if err := b.fs.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(f.path), err)
	}
	if err := WriteFileKeepMode(b.fs, f.path, f.after, filepath.Base(f.path), b.opts...); err != nil {
		return err
	}
	out.Written = append(out.Written, f.path)
	return nil
}
