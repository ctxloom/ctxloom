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
	skipDir   func(dir string) bool
	events    chan Event
	errs      chan error
	done      chan struct{}
	// pumped closes when the pump goroutine has returned, so Close can mean
	// "stopped" rather than "asked to stop".
	pumped    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// Option adjusts a Watcher at construction. Options exist so a caller that has
// nothing to say keeps the plain three-argument call.
type Option func(*Watcher)

// SkipDir excludes a directory and everything beneath it from a recursive
// watch: skip is called with each directory the walk reaches, and a true answer
// prunes that whole subtree.
//
// filter decides which events a caller is told about; it cannot decide which
// directories are worth watching, because a directory's own name rarely matches
// the paths the caller wants (a session directory is not a *.plan.md). Without
// this the two are conflated and a recursive watch spends an inotify watch on
// every directory under the root whichever way the filter answers — measured
// for `ctxloom plan watch`: 4,614 watched directories to observe 232 files.
//
// Pruning a subtree makes the watch blind to it, so this is a statement about
// where the caller's paths cannot appear, not an optimisation to apply
// speculatively.
func SkipDir(skip func(dir string) bool) Option {
	return func(w *Watcher) { w.skipDir = skip }
}

// New starts watching root. When recursive, every existing subdirectory — and
// any created later — is watched too, so changes at any depth are reported,
// except subtrees a SkipDir option prunes. filter, when non-nil, keeps only
// events whose path it accepts. root must already exist.
func New(root string, recursive bool, filter func(path string) bool, opts ...Option) (*Watcher, error) {
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
	for _, opt := range opts {
		opt(w)
	}
	if recursive {
		if err := w.addTree(root, nil); err != nil {
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

// addTree watches dir and every subdirectory beneath it that SkipDir does not
// prune, appending every non-directory it passes to existing when that is
// non-nil. Unreadable entries are skipped rather than aborting the walk.
func (w *Watcher) addTree(dir string, existing *[]string) error {
	return filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// The root is never pruned: refusing to watch what New was asked to
			// watch would be a silently empty watch, not an exclusion.
			if path != dir && w.skipDir != nil && w.skipDir(path) {
				return filepath.SkipDir
			}
			return w.fsw.Add(path)
		}
		if existing != nil {
			*existing = append(*existing, path)
		}
		return nil
	})
}

// pruned reports whether SkipDir excludes dir. A directory that appears after
// the watch starts has to be asked about here rather than inside addTree, whose
// walk always watches the directory it was handed.
func (w *Watcher) pruned(dir string) bool {
	return w.skipDir != nil && w.skipDir(dir)
}

// adopt starts watching a directory that appeared under an existing watch and
// returns the paths already inside it.
//
// A new directory is only ever learned about from its own Create event, so
// anything already in it by then was never observed appearing: a directory
// renamed into place, or written to between the mkdir and the fsw.Add, arrives
// already populated. Reporting those paths is what stops the watch from
// attaching to a directory whose contents it will never mention — the ctxloom
// session case, where the harp directory and its first plan file appear
// together. A path reported here may also produce its own event if it is
// written again; a duplicate is harmless where a silent omission is not.
func (w *Watcher) adopt(dir string) []string {
	var existing []string
	_ = w.addTree(dir, &existing)
	return existing
}

// deliver applies the filter and forwards path, reporting false once the
// watcher is closing and nothing further may be sent.
func (w *Watcher) deliver(path string) bool {
	if w.filter != nil && !w.filter(path) {
		return true
	}
	select {
	case w.events <- Event{Path: path}:
		return true
	case <-w.done:
		return false
	}
}

// pump is the watcher's only goroutine: it multiplexes the three ways a watch
// ends (closed by its owner, events channel closed, errors channel closed)
// against the two things it forwards. Each arm answers the same question — may
// this watcher keep going — so the loop itself carries no policy.
func (w *Watcher) pump() {
	defer close(w.pumped)
	defer close(w.events)
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok || !w.handleEvent(ev) {
				return
			}
		case err, ok := <-w.fsw.Errors:
			if !ok || !w.forwardError(err) {
				return
			}
		}
	}
}

// handleEvent forwards one fsnotify event, first adopting the directory it
// announces when the watch is recursive, so nested changes are caught without a
// restart. It reports false once the watcher is closing.
func (w *Watcher) handleEvent(ev fsnotify.Event) bool {
	if w.recursive && ev.Has(fsnotify.Create) && !w.pruned(ev.Name) {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			for _, path := range w.adopt(ev.Name) {
				if !w.deliver(path) {
					return false
				}
			}
		}
	}
	return w.deliver(ev.Name)
}

// forwardError hands one watch error to the consumer, reporting false once the
// watcher is closing.
//
// Every watch error is delivered, never dropped. A watch error is not
// decoration — "inotify queue overflow" means events were LOST, so discarding
// one leaves a watch that is silently missing changes looking exactly like a
// healthy quiet one. The send therefore blocks until the consumer takes it,
// with only Close as an escape: a consumer that stops reading Errors() stalls
// its own stream, which is visible, instead of losing failures, which is not.
func (w *Watcher) forwardError(err error) bool {
	select {
	case w.errs <- err:
		return true
	case <-w.done:
		return false
	}
}
