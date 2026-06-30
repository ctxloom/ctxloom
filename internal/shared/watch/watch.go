// Package watch provides a small fsnotify-based file/directory watcher shared by
// taskloom (the per-project task log) and ctxloom (session plan files), so the
// on-disk layout and the watching logic live in one place rather than being
// reimplemented — or pushed into a thin client — per consumer.
//
// It is deliberately generic: it reports raw filesystem change events for paths
// under a root (optionally recursively, including directories created after the
// watch starts), and each consumer translates those into its own domain's
// JSONL update stream.
package watch

import (
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// Op is a normalized filesystem operation, collapsing fsnotify's bitmask into a
// single dominant verb for the wire.
type Op string

const (
	OpCreate Op = "create"
	OpWrite  Op = "write"
	OpRemove Op = "remove"
	OpRename Op = "rename"
	OpChmod  Op = "chmod"
)

// Event is a single change to a watched path.
type Event struct {
	Path string
	Op   Op
}

// Watcher reports change Events for paths under a root until it is closed.
type Watcher struct {
	fsw       *fsnotify.Watcher
	recursive bool
	filter    func(path string) bool
	events    chan Event
	errs      chan error
	done      chan struct{}
}

// New starts watching root. When recursive, every existing subdirectory — and
// any created later — is watched too, so changes at any depth are reported.
// filter, when non-nil, keeps only events whose path it accepts. root is created
// if missing so the watch can attach before the first write.
func New(root string, recursive bool, filter func(path string) bool) (*Watcher, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fsw:       fsw,
		recursive: recursive,
		filter:    filter,
		events:    make(chan Event),
		errs:      make(chan error, 1),
		done:      make(chan struct{}),
	}
	if recursive {
		if err := w.addTree(root); err != nil {
			_ = fsw.Close()
			return nil, err
		}
	} else if err := fsw.Add(root); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	go w.pump()
	return w, nil
}

// Events delivers change events until the Watcher is closed.
func (w *Watcher) Events() <-chan Event { return w.events }

// Errors delivers watch errors (one is buffered) so a consumer can surface them.
func (w *Watcher) Errors() <-chan error { return w.errs }

// Close stops watching and releases the underlying resources.
func (w *Watcher) Close() error {
	close(w.done)
	return w.fsw.Close()
}

// addTree watches dir and every subdirectory beneath it. Unreadable entries are
// skipped rather than aborting the walk.
func (w *Watcher) addTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return w.fsw.Add(path)
		}
		return nil
	})
}

// pump forwards fsnotify events through the filter, adding watches for new
// directories when recursive so nested changes are caught without a restart.
func (w *Watcher) pump() {
	defer close(w.events)
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if w.recursive && ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = w.addTree(ev.Name)
				}
			}
			if w.filter != nil && !w.filter(ev.Name) {
				continue
			}
			select {
			case w.events <- Event{Path: ev.Name, Op: normalize(ev.Op)}:
			case <-w.done:
				return
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			select {
			case w.errs <- err:
			default:
			}
		}
	}
}

// normalize collapses fsnotify's op bitmask to a single dominant verb.
func normalize(op fsnotify.Op) Op {
	switch {
	case op.Has(fsnotify.Create):
		return OpCreate
	case op.Has(fsnotify.Write):
		return OpWrite
	case op.Has(fsnotify.Remove):
		return OpRemove
	case op.Has(fsnotify.Rename):
		return OpRename
	default:
		return OpChmod
	}
}
