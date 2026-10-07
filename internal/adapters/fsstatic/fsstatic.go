// Package fsstatic is the ONE static writer (delivery.Static): it drives the
// engine's typed approaches over a plan and leaves ONE ownership record per
// target file, tagged with the writer. The same value serves a session (the
// runner: root = session home, writer = the harp) and a human materialize
// (root = project root, writer = project); they differ only in the Target.
//
// A delivery is one batch (safefs.Batch): the writer's PREVIOUS claims under
// the target's roots are released, each item is delivered through the
// engine's approach for its kind — its claims staged as given, and every file
// it wrote over an overlay of the target filesystem staged as a claim on the
// whole file — and the batch commits, so a file several items or writers put
// values into is written once. A plan with no static items is UNINSTALL: the
// release alone runs. A delivery or a reversal holds every lock it takes
// until it has committed, in one order (lockScope).
package fsstatic

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// ErrInPlaceWrite refuses an approach that changes a file already standing on
// the target in place: opening it for writing, or setting its times or owner.
// The supported write is a temp file renamed into place
// (safefs.WriteFile and the helpers built on it).
var ErrInPlaceWrite = errors.New("an approach may not change an existing file in place; write it to a temp file and rename it into place (safefs.WriteFile)")

// The refusals of a delivery whose declaration (present.Delivered's Files and
// Claims) does not match what its approach did. Each fails the delivery,
// naming the approach and the path: the writer claims exactly what is
// declared, so a mismatch is a defect in the approach, never something to
// guess past.
var (
	// ErrUndeclaredWrite: a file written under a target root that Files does
	// not name. Claiming it would infer ownership the approach never stated.
	ErrUndeclaredWrite = errors.New("the approach wrote a file under a target root that it does not declare in Files")
	// ErrDeclaredUnclaimed: a file declared but not written, which the writer
	// holds no earlier whole-file claim on. Claiming it from its bytes on
	// disk would take a file nobody showed was ctxloom's.
	ErrDeclaredUnclaimed = errors.New("the approach declares a file it did not write and the writer holds no earlier whole-file claim on it")
	// ErrDeclaredMissing: a file declared but not written, whose earlier claim
	// names a file no longer standing.
	ErrDeclaredMissing = errors.New("the approach declares a file it did not write and the file does not stand on disk")
	// ErrDeclaredAndClaimed: a path both in Files and in Claims. A file is
	// owned whole or claimed into, never both.
	ErrDeclaredAndClaimed = errors.New("the approach both declares a file whole and claims values in it")
	// ErrDeclaredOutsideRoots: a path in Files outside every target root. A
	// file there is the approach's own state, never claimed or declared.
	ErrDeclaredOutsideRoots = errors.New("the approach declares a file outside every target root")
)

// Static is the writer over one Root: its filesystem, and the locks every
// writer of a target file takes.
type Static struct {
	fs    afero.Fs
	locks safefs.Locks
	// beforeCommit runs between a delivery's approach run and its commit; a
	// test parks a delivery there to interleave another with it. Nil in
	// production.
	beforeCommit func()
}

var _ delivery.Static = (*Static)(nil)

// New is the static writer over root.
func New(root safefs.Root) *Static { return &Static{fs: root.Fs, locks: root.Locks} }

// Deliver validates the target, refuses a plan whose items cannot root under
// it, then — holding the run's locks (lockScope) from before its first read
// until after its commit — releases the writer's previous delivery, stages
// each static item into one batch, and commits it.
func (s *Static) Deliver(_ context.Context, lo delivery.Loadout, root engine.Base, target delivery.Target) (out delivery.Delivered, err error) {
	if err := target.Validate(); err != nil {
		return delivery.Delivered{}, err
	}
	if err := planRootable(lo.Plan.Static, root.Surfaces(), target.Root.Paths()); err != nil {
		return delivery.Delivered{}, err
	}
	sc, err := s.begin()
	if err != nil {
		return delivery.Delivered{}, err
	}
	defer func() { err = errors.Join(err, sc.release()) }()
	return s.deliver(sc, lo, root, target)
}

// deliver is Deliver's run, under sc.
func (s *Static) deliver(sc *lockScope, lo delivery.Loadout, root engine.Base, target delivery.Target) (delivery.Delivered, error) {
	surfaces := root.Surfaces()
	paths := target.Root.Paths()
	within := func(path string) bool { return underARoot(paths, path) }
	undo := func(context.Context) error { return s.reverse(target.Ownership, within, target.Writer) }
	b := s.batch(sc)
	st := target.Ownership.In(b)
	if err := releaseWriters(st, target.Ownership, within, carried(lo.Package.CarryForward), target.Writer); err != nil {
		return delivery.Delivered{}, err
	}
	out := delivery.Delivered{Undo: undo}
	var modes map[string]os.FileMode
	if len(lo.Plan.Static) > 0 {
		inputs, err := delivery.InputsFor(lo, root.Dynamic)
		if err != nil {
			return delivery.Delivered{}, err
		}
		modes = map[string]os.FileMode{}
		for _, it := range lo.Plan.Static {
			d, err := s.deliverItem(sc, it, surfaces[it.Kind], target, inputs, st, modes)
			if err != nil {
				return delivery.Delivered{}, err
			}
			out.Presented = append(out.Presented, d.Presented)
			out.Wrote = append(out.Wrote, it.Kind)
		}
	}
	if s.beforeCommit != nil {
		s.beforeCommit()
	}
	if _, err := b.Commit(); err != nil {
		return delivery.Delivered{}, fmt.Errorf("fsstatic: deliver for %s: %w", target.Writer, err)
	}
	return out, s.restoreModes(modes)
}

// batch is a batch over the static writer's filesystem, each target locked
// by the lock every writer of that file takes, held in sc.
func (s *Static) batch(sc *lockScope) *safefs.Batch {
	return safefs.NewBatch(s.fs, func(path string, fn func() error) error { return sessions.WithFileLock(sc, path, fn) })
}

// restoreModes gives each file an approach wrote whole the mode it wrote it
// with (an exec bit on a skill's script is load-bearing): the batch writes
// the bytes and keeps an existing file's mode.
func (s *Static) restoreModes(modes map[string]os.FileMode) error {
	for _, path := range slices.Sorted(maps.Keys(modes)) {
		if err := s.fs.Chmod(path, modes[path]); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("fsstatic: mode of %s: %w", path, err)
		}
	}
	return nil
}

// planRootable refuses a plan item whose kind the engine declares no
// approach for, or whose root the target does not provide.
func planRootable(items []delivery.StaticItem, surfaces engine.Surfaces, paths present.Paths) error {
	for _, it := range items {
		if surfaces[it.Kind] == nil {
			return fmt.Errorf("fsstatic: the plan routes kind %v to approach %q but the engine declares no approach for it", it.Kind, it.Approach)
		}
		if !delivery.HasRoot(paths, it.Root) {
			return delivery.Unrootable{Kind: it.Kind, Approach: it.Approach, Needs: it.Root,
				Fix: fmt.Sprintf("the plan roots kind %v under %v and this target has no such root; select a root the target provides on the binding, or deliver to a target that has it", it.Kind, it.Root)}
		}
	}
	return nil
}

// deliverItem runs the item's approach over a copy-on-write overlay and
// stages what it delivered: its claims as given, and each file it wrote
// under a target root as a claim on the whole file. A file it wrote outside
// every root is its own state, written through as it wrote it.
func (s *Static) deliverItem(sc *lockScope, it delivery.StaticItem, approach present.Approach, target delivery.Target, inputs delivery.Inputs, st delivery.Staging, modes map[string]os.FileMode) (present.Delivered, error) {
	layer := &writeLayer{Fs: afero.NewMemMapFs(), names: map[string]struct{}{}}
	// The approach writes through the overlay but locks through the run's
	// scope over the real Locks: its own read-modify-writes exclude every
	// other writer, until the run has committed what it wrote.
	d, err := deliverKind(approach, it.Kind, target.Root, it.Root, inputs, safefs.Root{Fs: newOverlay(s.fs, layer), Locks: sc})
	if err != nil {
		return present.Delivered{}, fmt.Errorf("fsstatic: deliver %v through %s: %w", it.Kind, it.Approach, err)
	}
	for _, path := range slices.Sorted(maps.Keys(d.Claims)) {
		if err := st.Stage(path, target.Writer, d.Claims[path]); err != nil {
			return present.Delivered{}, fmt.Errorf("fsstatic: stage %v's claims on %s: %w", it.Kind, path, err)
		}
	}
	for _, path := range layer.files() {
		if _, claimed := d.Claims[path]; claimed {
			return present.Delivered{}, fmt.Errorf("fsstatic: %s both claims values in %s and writes it whole; an approach does one or the other", it.Approach, path)
		}
		if err := s.stageWritten(layer, path, target, st, modes); err != nil {
			return present.Delivered{}, err
		}
	}
	return d, nil
}

// stageWritten stages one file an approach wrote as a claim on the whole
// file, keeping the mode it wrote it with; a file outside every target root
// is the approach's own state, written through as it wrote it.
func (s *Static) stageWritten(layer afero.Fs, path string, target delivery.Target, st delivery.Staging, modes map[string]os.FileMode) error {
	bytes, err := afero.ReadFile(layer, path)
	if err != nil {
		return err
	}
	info, err := layer.Stat(path)
	if err != nil {
		return err
	}
	if !underARoot(target.Root.Paths(), path) {
		if err := writeThrough(s.fs, path, bytes, info.Mode().Perm()); err != nil {
			return fmt.Errorf("fsstatic: write %s: %w", path, err)
		}
		return nil
	}
	if err := st.Stage(path, target.Writer, []present.Claim{{Value: bytes}}); err != nil {
		return fmt.Errorf("fsstatic: stage %s for %s: %w", path, target.Writer, err)
	}
	modes[path] = info.Mode().Perm()
	return nil
}

// carried keeps a claim made through one of the sources the package carries
// forward (composite.Package.CarryForward): a companion whose probe failed
// keeps its entries as the record says it left them.
func carried(sources []string) func(pointer, via string) bool {
	return func(_, via string) bool { return slices.Contains(sources, via) }
}

// releaseWriters stages the release of each writer's claims in every file the
// record names for it that within admits, keeping the claims keep names.
func releaseWriters(st delivery.Staging, ownership delivery.Ownership, within func(string) bool, keep func(pointer, via string) bool, writers ...delivery.Writer) error {
	for _, w := range writers {
		targets, err := ownership.Targets(w)
		if err != nil {
			return err
		}
		for _, path := range targets {
			if !within(path) {
				continue
			}
			if err := st.Release(path, w, keep); err != nil {
				return fmt.Errorf("fsstatic: release %s for %s: %w", path, w, err)
			}
		}
	}
	return nil
}

// Reverse takes each writer's claims back out of every file the record names
// for it, in one batch (delivery.Static.Reverse).
func (s *Static) Reverse(_ context.Context, ownership delivery.Ownership, writers ...delivery.Writer) error {
	return s.reverse(ownership, func(string) bool { return true }, writers...)
}

// reverse releases writers from each file within admits, in one batch,
// holding the run's locks (lockScope) until it has committed.
func (s *Static) reverse(ownership delivery.Ownership, within func(string) bool, writers ...delivery.Writer) (err error) {
	sc, err := s.begin()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, sc.release()) }()
	b := s.batch(sc)
	if err := releaseWriters(ownership.In(b), ownership, within, nil, writers...); err != nil {
		return err
	}
	if _, err := b.Commit(); err != nil {
		return fmt.Errorf("fsstatic: reverse %v: %w", writers, err)
	}
	return nil
}

// deliverKind hands the kind's inputs to the approach's typed Deliver. The
// approach table is keyed by kind and each approach satisfies its kind's
// interface (the Definition's typed fields made any other pairing a compile
// error), so a miss here is a programming error in the engine, reported.
func deliverKind(a present.Approach, kind present.Kind, start present.Start, root present.RootKind, in delivery.Inputs, files safefs.Root) (present.Delivered, error) {
	fs := files.Fs
	var (
		d   present.Delivered
		ok  bool
		err error
	)
	switch kind {
	case present.Context:
		d, ok, err = deliverAs(a, func(c engine.ContextApproach) (present.Delivered, error) {
			return c.DeliverContext(start, root, in.Context, fs)
		})
	case present.MCP:
		d, ok, err = deliverAs(a, func(c engine.MCPApproach) (present.Delivered, error) { return c.DeliverMCP(start, root, in.MCP, fs) })
	case present.Settings:
		d, ok, err = deliverAs(a, func(c engine.SettingsApproach) (present.Delivered, error) {
			return c.DeliverSettings(start, root, in.Settings, fs)
		})
	case present.Hooks:
		d, ok, err = deliverAs(a, func(c engine.HooksApproach) (present.Delivered, error) {
			return c.DeliverHooks(start, root, in.Hooks, fs)
		})
	case present.Commands:
		d, ok, err = deliverAs(a, func(c engine.CommandsApproach) (present.Delivered, error) {
			return c.DeliverCommands(start, root, in.Commands, files)
		})
	case present.Skills:
		d, ok, err = deliverAs(a, func(c engine.SkillsApproach) (present.Delivered, error) {
			return c.DeliverSkills(start, root, in.Skills, files)
		})
	}
	if !ok {
		return present.Delivered{}, fmt.Errorf("approach %q does not deliver kind %v", a.Name(), kind)
	}
	return d, err
}

// deliverAs delivers through a when it implements kind interface A; ok is
// false when it does not.
func deliverAs[A any](a present.Approach, deliver func(A) (present.Delivered, error)) (d present.Delivered, ok bool, err error) {
	c, ok := a.(A)
	if !ok {
		return present.Delivered{}, false, nil
	}
	d, err = deliver(c)
	return d, true, err
}

// overlay is what an approach delivers through: afero's CopyOnWriteFs over
// the target with the write layer on top, REFUSING to open a file that
// stands on the target for writing, or to set its times or owner.
//
// That refusal is the whole reason this type exists. afero serves each of
// those by copying the file up through the layer's Create, which carries no
// mode, so the mode the static writer restores would be 0. Chmod is left alone: it
// copies up too, but then sets the mode itself. No approach writes in
// place — each writes a temp file and renames it — so the capability is
// removed rather than repaired.
type overlay struct {
	*afero.CopyOnWriteFs
	base afero.Fs
}

func newOverlay(base, layer afero.Fs) *overlay {
	return &overlay{CopyOnWriteFs: afero.NewCopyOnWriteFs(base, layer).(*afero.CopyOnWriteFs), base: base}
}

// writeFlags are the open flags afero treats as a write, and so as a copy-up
// of a base file.
const writeFlags = os.O_WRONLY | os.O_RDWR | os.O_APPEND | os.O_CREATE | os.O_TRUNC

func (o *overlay) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&writeFlags != 0 {
		if err := o.refuseInPlace("open for writing", name); err != nil {
			return nil, err
		}
	}
	return o.CopyOnWriteFs.OpenFile(name, flag, perm)
}

// Create is overridden because afero's Create calls its OWN OpenFile, which
// would bypass the refusal.
func (o *overlay) Create(name string) (afero.File, error) {
	if err := o.refuseInPlace("create", name); err != nil {
		return nil, err
	}
	return safefs.Create(o.CopyOnWriteFs, name)
}

func (o *overlay) Chtimes(name string, atime, mtime time.Time) error {
	if err := o.refuseInPlace("chtimes", name); err != nil {
		return err
	}
	return o.CopyOnWriteFs.Chtimes(name, atime, mtime)
}

func (o *overlay) Chown(name string, uid, gid int) error {
	if err := o.refuseInPlace("chown", name); err != nil {
		return err
	}
	return o.CopyOnWriteFs.Chown(name, uid, gid)
}

// refuseInPlace refuses name when the base holds it; a name the base does
// not hold is a new file.
func (o *overlay) refuseInPlace(op, name string) error {
	if _, err := o.base.Stat(name); err != nil {
		return nil
	}
	return &os.PathError{Op: op, Path: name, Err: ErrInPlaceWrite}
}

// writeLayer is the overlay's write layer, noting every name the overlay
// puts content under: a create, an open for writing (a copy-up included),
// or a rename's destination.
//
// The written files are found by name, never by walking the layer: afero's
// MemMapFs roots its tree at a lone separator, and a Windows volume path
// (C:\...) is its own parent there, so a walk from the root finds nothing and
// every delivery would land no file at all.
type writeLayer struct {
	afero.Fs
	names map[string]struct{}
}

func (w *writeLayer) note(name string) { w.names[filepath.Clean(name)] = struct{}{} }

func (w *writeLayer) Create(name string) (afero.File, error) {
	w.note(name)
	return safefs.Create(w.Fs, name)
}

func (w *writeLayer) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		w.note(name)
	}
	return w.Fs.OpenFile(name, flag, perm)
}

func (w *writeLayer) Rename(oldname, newname string) error {
	w.note(newname)
	return safefs.Rename(w.Fs, oldname, newname)
}

// files lists every noted name the layer still holds as a regular file,
// sorted: a name since renamed away or removed is not a written file.
func (w *writeLayer) files() []string {
	var out []string
	for name := range w.names {
		if info, err := w.Stat(name); err == nil && info.Mode().IsRegular() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// writeThrough lands a file the approach wrote outside the target's roots
// on the real filesystem, bytes and mode as written. That file is the
// approach's own state, which may hold anything the approach keeps, so a
// directory created for it is owner-only by mode (safefs.PrivateDirMode: on
// Windows no DACL is applied, as these paths lie outside ctxloom's
// established home roots). A directory that already exists is left as it
// is: this lands a file, it does not own the directory it lands in.
func writeThrough(fs afero.Fs, path string, bytes []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	exists, err := afero.DirExists(fs, dir)
	if err != nil {
		return err
	}
	if !exists {
		if err := fs.MkdirAll(dir, safefs.PrivateDirMode); err != nil {
			return err
		}
	}
	return safefs.WriteFile(fs, path, bytes, mode)
}

// rootsOf are the target's resolved roots, in the order a written file is
// attributed to them.
func rootsOf(paths present.Paths) []string {
	return []string{paths.SessionHome.Host, paths.ProjectRoot.Host}
}

// underARoot reports whether path lies under one of the target's roots.
func underARoot(paths present.Paths, path string) bool {
	for _, root := range rootsOf(paths) {
		if root != "" && present.Under(path, root) {
			return true
		}
	}
	return false
}
