// Package deliverytest is the fixture half of the delivery port's tests: an
// in-memory Ownership whose records a test can read whole, and the file
// listing the tests compare deliveries by.
package deliverytest

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Ownership is the in-memory record: per target file, per writer, the
// entries that writer owns, and whether ctxloom created the file. It writes
// and removes target files on the fs it was given, so a test observes the
// same reconcile-to-empty a production record performs.
type Ownership struct {
	fs afero.Fs

	mu      sync.Mutex
	entries map[string]map[delivery.Writer][]string
	created map[string]bool
	calls   []string
}

var _ delivery.Ownership = (*Ownership)(nil)

// NewOwnership is an empty record over fs.
func NewOwnership(fs afero.Fs) *Ownership {
	return &Ownership{fs: fs, entries: map[string]map[delivery.Writer][]string{}, created: map[string]bool{}}
}

// Prepare records that it was called; the in-memory record has no storage
// to ready.
func (o *Ownership) Prepare(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, CallPrepare)
	return nil
}

// The calls Calls reports, in the order the record received them.
const (
	CallPrepare = "prepare"
	CallApply   = "apply"
)

// Calls is the sequence of Prepare and Apply calls the record received, so a
// test can pin that a delivery prepares before it writes.
func (o *Ownership) Calls() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

// Apply reads the target, hands it to build and records the writer's
// entries. A nil desired reconciles the writer to empty; a file ctxloom
// created that no writer owns entries in any longer is removed.
func (o *Ownership) Apply(_ context.Context, fsys afero.Fs, target string, writer delivery.Writer, build delivery.Build) (delivery.Result, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, CallApply)
	current, err := afero.ReadFile(fsys, target)
	existed := err == nil
	if err != nil && !isNotExist(err) {
		return delivery.Result{}, err
	}
	desired, entries, err := build(current)
	if err != nil {
		return delivery.Result{}, err
	}
	if desired == nil {
		return o.reconcileLocked(fsys, target, writer, existed)
	}
	o.recordLocked(target, writer, entries, existed)
	if existed && slices.Equal(current, desired) {
		return delivery.Result{}, nil
	}
	if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return delivery.Result{}, err
	}
	return delivery.Result{Changed: true}, iox.WriteFileAtomicFs(fsys, target, desired, 0o644)
}

// reconcileLocked takes writer's entries out of target; the last writer of a
// file ctxloom created removes it. Caller holds o.mu.
func (o *Ownership) reconcileLocked(fsys afero.Fs, target string, writer delivery.Writer, existed bool) (delivery.Result, error) {
	if o.entries[target] == nil {
		return delivery.Result{}, nil
	}
	delete(o.entries[target], writer)
	if len(o.entries[target]) > 0 {
		return delivery.Result{}, nil
	}
	delete(o.entries, target)
	if o.created[target] && existed {
		delete(o.created, target)
		return delivery.Result{Changed: true}, fsys.Remove(target)
	}
	return delivery.Result{}, nil
}

// recordLocked records writer's entries in target, marking a file this
// write creates. Caller holds o.mu.
func (o *Ownership) recordLocked(target string, writer delivery.Writer, entries []string, existed bool) {
	if !existed {
		o.created[target] = true
	}
	if o.entries[target] == nil {
		o.entries[target] = map[delivery.Writer][]string{}
	}
	o.entries[target][writer] = slices.Clone(entries)
}

// Owned is one writer's entries in one target file.
func (o *Ownership) Owned(target string, writer delivery.Writer) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.entries[target][writer]), nil
}

// Targets lists the files a writer owns entries in, sorted.
func (o *Ownership) Targets(writer delivery.Writer) ([]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for target, byWriter := range o.entries {
		if _, ok := byWriter[writer]; ok {
			out = append(out, target)
		}
	}
	sort.Strings(out)
	return out, nil
}

// AllOwned is every entry a writer owns across every target, sorted: what
// the tests compare against the files under a root.
func (o *Ownership) AllOwned(writer delivery.Writer) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, byWriter := range o.entries {
		out = append(out, byWriter[writer]...)
	}
	sort.Strings(out)
	return out
}

func isNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// RelativeFiles lists every regular file under root, relative to it, in
// sorted order; nil when the root holds none.
func RelativeFiles(fsys afero.Fs, root string) []string {
	var out []string
	_ = afero.Walk(fsys, root, func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out
}
