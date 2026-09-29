// Package fsstatic is the ONE static writer (delivery.Static): it drives the
// engine's typed approaches over a plan and leaves ONE ownership record per
// target file, tagged with the writer. The same value serves a session (the
// runner: root = session home, writer = the harp) and a human materialize
// (root = project root, writer = project); they differ only in the Target.
//
// A delivery is reconcile-from-clean: the writer's PREVIOUS contribution is
// reversed through the record first, then each item is delivered through
// the engine's approach for its kind over an overlay of the target
// filesystem, and every file the approach wrote is recorded under the
// writer through Ownership.Apply — the record computes the reversal from
// the before and after images, so an approach needs to know nothing about
// ownership. A plan with no static items is UNINSTALL: the reversal runs
// and nothing else is touched.
package fsstatic

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ErrInPlaceWrite refuses an approach that opens a file already standing on
// the target for writing in place. The supported write is a temp file
// renamed into place (iox.WriteFileAtomicFs and the helpers built on it).
var ErrInPlaceWrite = errors.New("an approach may not open an existing file for writing in place; write it to a temp file and rename it into place (iox.WriteFileAtomicFs)")

// Static is the writer over one filesystem.
type Static struct{ fs afero.Fs }

var _ delivery.Static = (*Static)(nil)

// New is the static writer over fs.
func New(fs afero.Fs) *Static { return &Static{fs: fs} }

// Deliver validates the target, refuses a plan whose items cannot root under
// it, prepares the ownership record (delivery.Ownership.Prepare: a security
// invariant, not an optimisation), reverses the writer's previous delivery,
// then delivers each static item and records what it wrote.
func (s *Static) Deliver(ctx context.Context, lo delivery.Loadout, root engine.Base, target delivery.Target) (delivery.Delivered, error) {
	if err := target.Validate(); err != nil {
		return delivery.Delivered{}, err
	}
	surfaces := root.Surfaces()
	if err := planRootable(lo.Plan.Static, surfaces, target.Root.Paths()); err != nil {
		return delivery.Delivered{}, err
	}
	if err := target.Ownership.Prepare(ctx); err != nil {
		return delivery.Delivered{}, fmt.Errorf("fsstatic: prepare the ownership record for %s: %w", target.Writer, err)
	}
	undo := func(ctx context.Context) error { return s.reconcileToEmpty(ctx, target) }
	if err := s.reconcileToEmpty(ctx, target); err != nil {
		return delivery.Delivered{}, err
	}
	if len(lo.Plan.Static) == 0 {
		return delivery.Delivered{Undo: undo}, nil
	}
	inputs, err := delivery.InputsFor(lo, root.Dynamic)
	if err != nil {
		return delivery.Delivered{}, err
	}
	out := delivery.Delivered{Undo: undo}
	for _, it := range lo.Plan.Static {
		d, err := s.deliverItem(ctx, it, surfaces[it.Kind], target, inputs)
		if err != nil {
			return delivery.Delivered{}, err
		}
		out.Presented = append(out.Presented, d.Presented)
		out.Wrote = append(out.Wrote, it.Kind)
	}
	return out, nil
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

// deliverItem runs the item's approach over a copy-on-write overlay, then
// lands every file it wrote.
func (s *Static) deliverItem(ctx context.Context, it delivery.StaticItem, approach present.Approach, target delivery.Target, inputs delivery.Inputs) (present.Delivered, error) {
	layer := &writeLayer{Fs: afero.NewMemMapFs(), names: map[string]struct{}{}}
	d, err := deliverKind(approach, it.Kind, target.Root, it.Root, inputs, newOverlay(s.fs, layer))
	if err != nil {
		return present.Delivered{}, fmt.Errorf("fsstatic: deliver %v through %s: %w", it.Kind, it.Approach, err)
	}
	for _, path := range layer.files() {
		if err := s.landFile(ctx, layer, path, target); err != nil {
			return present.Delivered{}, err
		}
	}
	return d, nil
}

// landFile moves one file the approach wrote from the overlay layer onto the
// filesystem: under a target root through the ownership record, with the
// approach's mode; outside every root written through as-is.
func (s *Static) landFile(ctx context.Context, layer afero.Fs, path string, target delivery.Target) error {
	bytes, err := afero.ReadFile(layer, path)
	if err != nil {
		return err
	}
	info, err := layer.Stat(path)
	if err != nil {
		return err
	}
	paths := target.Root.Paths()
	if !underARoot(paths, path) {
		// An approach's OWN state outside the target (claude's MCP
		// approach keeps a hew record under the home): written
		// through as the approach wrote it, never a delivered file
		// the record owns.
		if err := writeThrough(s.fs, path, bytes, info.Mode().Perm()); err != nil {
			return fmt.Errorf("fsstatic: write %s: %w", path, err)
		}
		return nil
	}
	entry := relativeTo(paths, path)
	if _, err := target.Ownership.Apply(ctx, s.fs, path, target.Writer, func([]byte) ([]byte, []string, error) {
		return bytes, []string{entry}, nil
	}); err != nil {
		return fmt.Errorf("fsstatic: record %s for %s: %w", path, target.Writer, err)
	}
	// The record writes the bytes; the mode is the approach's (an
	// exec bit on a skill's script is load-bearing).
	if err := s.fs.Chmod(path, info.Mode().Perm()); err != nil {
		return fmt.Errorf("fsstatic: mode of %s: %w", path, err)
	}
	return nil
}

// reconcileToEmpty reverses the writer's contribution to every file its
// record names UNDER THIS TARGET's roots: the record is home-rooted and one
// writer (the project's) delivers into many projects, so a target's empty
// plan reaches its own files and no other project's.
func (s *Static) reconcileToEmpty(ctx context.Context, target delivery.Target) error {
	targets, err := target.Ownership.Targets(target.Writer)
	if err != nil {
		return err
	}
	paths := target.Root.Paths()
	for _, path := range targets {
		if !underARoot(paths, path) {
			continue
		}
		if _, err := target.Ownership.Apply(ctx, s.fs, path, target.Writer, func([]byte) ([]byte, []string, error) {
			return nil, nil, nil
		}); err != nil {
			return fmt.Errorf("fsstatic: reconcile %s for %s: %w", path, target.Writer, err)
		}
	}
	return nil
}

// deliverKind hands the kind's inputs to the approach's typed Deliver. The
// approach table is keyed by kind and each approach satisfies its kind's
// interface (the Definition's typed fields made any other pairing a compile
// error), so a miss here is a programming error in the engine, reported.
func deliverKind(a present.Approach, kind present.Kind, start present.Start, root present.RootKind, in delivery.Inputs, fs afero.Fs) (present.Delivered, error) {
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
			return c.DeliverCommands(start, root, in.Commands, fs)
		})
	case present.Skills:
		d, ok, err = deliverAs(a, func(c engine.SkillsApproach) (present.Delivered, error) {
			return c.DeliverSkills(start, root, in.Skills, fs)
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
// stands on the target for writing.
//
// That refusal is the whole reason this type exists. afero serves such an
// open by copying the file up through the layer's Create, which carries no
// mode, so landFile would chmod the real file to 0. No approach writes in
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
		if err := o.refuseInPlace(name); err != nil {
			return nil, err
		}
	}
	return o.CopyOnWriteFs.OpenFile(name, flag, perm)
}

// Create is overridden because afero's Create calls its OWN OpenFile, which
// would bypass the refusal.
func (o *overlay) Create(name string) (afero.File, error) {
	if err := o.refuseInPlace(name); err != nil {
		return nil, err
	}
	return iox.Create(o.CopyOnWriteFs, name)
}

// refuseInPlace refuses name when the base holds it; a name the base does
// not hold is a new file.
func (o *overlay) refuseInPlace(name string) error {
	if _, err := o.base.Stat(name); err != nil {
		return nil
	}
	return &os.PathError{Op: "open for writing", Path: name, Err: ErrInPlaceWrite}
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
	return iox.Create(w.Fs, name)
}

func (w *writeLayer) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE) != 0 {
		w.note(name)
	}
	return w.Fs.OpenFile(name, flag, perm)
}

func (w *writeLayer) Rename(oldname, newname string) error {
	w.note(newname)
	return iox.Rename(w.Fs, oldname, newname)
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
// approach's own state (claude's undo record, which keeps the previous value
// of the key it undoes), so a directory created for it is owner-only, through
// the record store's own seam. A directory that already exists is left as it
// is: this lands a file, it does not own the directory it lands in.
func writeThrough(fs afero.Fs, path string, bytes []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	exists, err := afero.DirExists(fs, dir)
	if err != nil {
		return err
	}
	if !exists {
		if err := confpatch.EnsureRecordDir(fs, dir); err != nil {
			return err
		}
	}
	return iox.WriteFileAtomicFs(fs, path, bytes, mode)
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

// relativeTo names a written file as the record's entry: its path relative
// to the target root it lies under, or the path itself when it lies under
// none.
func relativeTo(paths present.Paths, path string) string {
	for _, root := range rootsOf(paths) {
		if root == "" || !present.Under(path, root) {
			continue
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return path
}
