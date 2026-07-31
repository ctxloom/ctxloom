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
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Event is a single change to a watched path. Op-level detail (create/write/
// remove/...) was deleted (U132-F04): both consumers (cmd/taskloom watch,
// ctxloom plan watch) discard the whole event and emit a fixed
// {"event":"changed"} line — fsnotify's bitmask never reached a wire. Add it
// back only alongside a real consumer.
type Event struct {
	Path string
}

// Watcher reports change Events for paths under a root until it is closed.
type Watcher struct {
	fsw       *fsnotify.Watcher
	recursive bool
	filter    func(path string) bool
	events    chan Event
	errs      chan error
	done      chan struct{}
	// pumped closes when the pump goroutine has returned, so Close can mean
	// "stopped" rather than "asked to stop".
	pumped    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// New starts watching root. When recursive, every existing subdirectory — and
// any created later — is watched too, so changes at any depth are reported.
// filter, when non-nil, keeps only events whose path it accepts. root is created
// if missing so the watch can attach before the first write.
func New(root string, recursive bool, filter func(path string) bool) (*Watcher, error) {
	// U132-F03: New used to os.MkdirAll(root) unconditionally, so a
	// nonexistent, typo'd, or wrongly-resolved root produced a healthy-
	// looking watcher on an empty directory that streams zero events
	// forever, at exit 0 — indistinguishable from a correct-but-quiet watch,
	// and a read-shaped operation mutating the filesystem as a side effect.
	// A caller that genuinely needs the directory to exist (a log file whose
	// directory may not have been created yet) creates it explicitly at its
	// own call site, where that intent is local and reviewable.
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("watch %s: not a directory", root)
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
		pumped:    make(chan struct{}),
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

// Errors delivers every watch error (one is buffered, the rest wait) so a
// consumer can surface them. Nothing is discarded: see pump.
func (w *Watcher) Errors() <-chan error { return w.errs }

// stopped reports whether Close has been called, so a consumer can tell a
// caller-requested shutdown from the watcher dying underneath it — the event
// channel closes either way.
func (w *Watcher) stopped() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// Close stops watching, waits for the pump to stop, and releases the underlying
// resources. It is idempotent and safe to call from more than one owner: a
// watcher is routinely held by both the caller that deferred a Close and the
// stream driving it, and neither can ask whether the other has already stopped
// it. Every call returns the same error the first one produced.
//
// Close returns only once the pump goroutine has returned, so "Close returned"
// means the watcher has stopped rather than been asked to.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
		w.closeErr = w.fsw.Close()
		<-w.pumped
	})
	return w.closeErr
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
	defer close(w.pumped)
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
			case w.events <- Event{Path: ev.Name}:
			case <-w.done:
				return
			}
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// Every watch error is delivered, never dropped. A watch error is
			// not decoration — "inotify queue overflow" means events were LOST,
			// so discarding one leaves a watch that is silently missing changes
			// looking exactly like a healthy quiet one. The send therefore
			// blocks until the consumer takes it, with only Close as an escape:
			// a consumer that stops reading Errors() stalls its own stream,
			// which is visible, instead of losing failures, which is not.
			select {
			case w.errs <- err:
			case <-w.done:
				return
			}
		}
	}
}
