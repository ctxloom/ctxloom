// Package fsstatic is the ONE static writer (delivery.Static): it drives the
// engine's typed approaches over a plan and leaves ONE ownership record per
// target file, tagged with the writer. The same value serves a session (the
// runner: root = session home, writer = the harp) and a human materialize
// (root = project root, writer = project); they differ only in the Target.
//
// A delivery is one batch (safefs.Batch). Each item is delivered through the
// engine's approach for its kind, over an overlay of the target filesystem,
// and the approach DECLARES what it owns (present.Delivered's Files and
// Claims); ownership is never inferred from what it happened to write. The
// writer's previous claims under the target's roots are released, except on
// a declared file the approach did not rewrite, and the declaration is
// staged; the batch commits, so a file several items or writers put values
// into is written once. A plan with no static items is UNINSTALL: the
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

// ErrKindNotTargeted refuses a plan item of a kind the target does not speak
// for (delivery.Target.Kinds): its claims would land under a writer this
// target never releases.
var ErrKindNotTargeted = errors.New("fsstatic: the plan routes a kind the target does not speak for")

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
	for _, it := range lo.Plan.Static {
		if !target.Speaks(it.Kind) {
			return delivery.Delivered{}, fmt.Errorf("%w: %v (the target speaks for %v)", ErrKindNotTargeted, it.Kind, target.Kinds)
		}
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

// deliver is Deliver's run, under sc, in three phases. A: each approach
// runs over its own overlay, and what it declared is checked against what it
// wrote (ran.check) and against the record (retain). B: the writer's earlier
// claims under the target's roots are released, except on the files it
// retains. C: each declaration is staged. Every release is staged before any
// stage because the record folds a file's operations in order: a release
// staged after a claim on the same file would drop that claim.
func (s *Static) deliver(sc *lockScope, lo delivery.Loadout, root engine.Base, target delivery.Target) (delivery.Delivered, error) {
	paths := target.Root.Paths()
	within := func(path string) bool { return underARoot(paths, path) }
	out := delivery.Delivered{Undo: func(context.Context) error { return s.reverse(target.Ownership, within, target.Writers()...) }}
	runs, err := s.runAll(sc, lo, root, target)
	if err != nil {
		return delivery.Delivered{}, err
	}
	for _, r := range runs {
		out.Presented = append(out.Presented, r.d.Presented)
		out.Wrote = append(out.Wrote, r.kind)
	}
	retained, err := s.retain(runs, target)
	if err != nil {
		return delivery.Delivered{}, err
	}
	b := s.batch(sc)
	st := target.Ownership.In(b)
	released := func(path string) bool { return within(path) && !retained[path] }
	if err := releaseWriters(st, target.Ownership, released, carried(lo.Package.CarryForward), target.Writers()...); err != nil {
		return delivery.Delivered{}, err
	}
	modes := map[string]os.FileMode{}
	for _, r := range runs {
		if err := r.stage(st, target, modes); err != nil {
			return delivery.Delivered{}, err
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

// runAll is phase A's run: each static item's approach over its own
// overlay, its declaration checked against what it wrote, and the files it
// wrote outside every root written through as its own state.
func (s *Static) runAll(sc *lockScope, lo delivery.Loadout, root engine.Base, target delivery.Target) ([]ran, error) {
	if len(lo.Plan.Static) == 0 {
		return nil, nil
	}
	inputs, err := delivery.InputsFor(lo, root.Dynamic)
	if err != nil {
		return nil, err
	}
	surfaces := root.Surfaces()
	runs := make([]ran, 0, len(lo.Plan.Static))
	for _, it := range lo.Plan.Static {
		r, err := s.run(sc, it, surfaces[it.Kind], target, inputs)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	for _, r := range runs {
		if err := s.writeOwnState(r); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

// ran is one approach's run: its declaration, and the files it wrote over
// its overlay, split into those under a target root (each must be declared,
// and is claimed whole) and its own state outside every root.
type ran struct {
	approach string
	kind     present.Kind
	d        present.Delivered
	layer    *writeLayer
	written  []string // under a target root, sorted
	own      []string // outside every target root, sorted
}

// run runs the item's approach over a copy-on-write overlay and checks its
// declaration against what it wrote.
func (s *Static) run(sc *lockScope, it delivery.StaticItem, approach present.Approach, target delivery.Target, inputs delivery.Inputs) (ran, error) {
	layer := &writeLayer{Fs: afero.NewMemMapFs(), names: map[string]struct{}{}}
	// The approach writes through the overlay but locks through the run's
	// scope over the real Locks: its own read-modify-writes exclude every
	// other writer, until the run has committed what it wrote.
	d, err := deliverKind(approach, it.Kind, target.Root, it.Root, inputs, safefs.Root{Fs: newOverlay(s.fs, layer), Locks: sc})
	if err != nil {
		return ran{}, fmt.Errorf("fsstatic: deliver %v through %s: %w", it.Kind, it.Approach, err)
	}
	r := ran{approach: it.Approach, kind: it.Kind, d: d, layer: layer}
	paths := target.Root.Paths()
	for _, path := range layer.files() {
		if underARoot(paths, path) {
			r.written = append(r.written, path)
		} else {
			r.own = append(r.own, path)
		}
	}
	return r, r.check(paths)
}

// refuse is a refusal of r's declaration at path.
func (r ran) refuse(path string, why error) error {
	return fmt.Errorf("fsstatic: %v through %s: %s: %w", r.kind, r.approach, path, why)
}

// check holds r's declaration to what it wrote: every declared file lies
// under a root and is not also claimed into, and every file written under a
// root is declared.
func (r ran) check(paths present.Paths) error {
	declared := make(map[string]bool, len(r.d.Files))
	for _, path := range r.d.Files {
		path = filepath.Clean(path)
		if !underARoot(paths, path) {
			return r.refuse(path, ErrDeclaredOutsideRoots)
		}
		if _, claimed := r.d.Claims[path]; claimed {
			return r.refuse(path, ErrDeclaredAndClaimed)
		}
		declared[path] = true
	}
	for _, path := range r.written {
		if !declared[path] {
			return r.refuse(path, ErrUndeclaredWrite)
		}
	}
	return nil
}

// retain is every file a run declares and no run wrote: each keeps the
// writer's earlier whole-file claim, so the writer must hold one and the
// file must stand. It is never claimed from its bytes on disk: a file the
// user has since edited would then be judged against that new claim
// (targetOps.wholeFileEdited) and taken as ctxloom's.
func (s *Static) retain(runs []ran, target delivery.Target) (map[string]bool, error) {
	written := map[string]bool{}
	for _, r := range runs {
		for _, path := range r.written {
			written[path] = true
		}
	}
	retained := map[string]bool{}
	for _, r := range runs {
		for _, path := range r.d.Files {
			path = filepath.Clean(path)
			if written[path] || retained[path] {
				continue
			}
			if err := s.retainable(target.WriterOf(r.kind), target.Ownership, path); err != nil {
				return nil, r.refuse(path, err)
			}
			retained[path] = true
		}
	}
	return retained, nil
}

// retainable is nil when the record holds writer's claim on path whole and
// the file stands.
func (s *Static) retainable(writer delivery.Writer, ownership delivery.Ownership, path string) error {
	states, err := ownership.Paths(s.fs, path)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(states, func(st delivery.PathState) bool {
		return st.Pointer == "" && slices.Contains(st.Writers, writer)
	}) {
		return ErrDeclaredUnclaimed
	}
	exists, err := afero.Exists(s.fs, path)
	if err != nil {
		return err
	}
	if !exists {
		return ErrDeclaredMissing
	}
	return nil
}

// writeOwnState lands each file r wrote outside every target root on the
// real filesystem as it wrote it: the approach's own state, never claimed.
func (s *Static) writeOwnState(r ran) error {
	for _, path := range r.own {
		bytes, info, err := readWritten(r.layer, path)
		if err != nil {
			return err
		}
		if err := writeThrough(s.fs, path, bytes, info.Mode().Perm()); err != nil {
			return fmt.Errorf("fsstatic: write %s: %w", path, err)
		}
	}
	return nil
}

// stage stages r's declaration under the target's writer for r's kind
// (Target.WriterOf): its claims as
// given, and each file it wrote under a root as a claim on the whole file,
// noting the mode it wrote it with. A retained file is staged nothing: the
// release passed over it, so its earlier claim stands as it was.
func (r ran) stage(st delivery.Staging, target delivery.Target, modes map[string]os.FileMode) error {
	writer := target.WriterOf(r.kind)
	for _, path := range slices.Sorted(maps.Keys(r.d.Claims)) {
		if err := st.Stage(path, writer, r.d.Claims[path]); err != nil {
			return fmt.Errorf("fsstatic: stage %v's claims on %s: %w", r.kind, path, err)
		}
	}
	for _, path := range r.written {
		bytes, info, err := readWritten(r.layer, path)
		if err != nil {
			return err
		}
		if err := st.Stage(path, writer, []present.Claim{{Value: bytes}}); err != nil {
			return fmt.Errorf("fsstatic: stage %s for %s: %w", path, writer, err)
		}
		modes[path] = info.Mode().Perm()
	}
	return nil
}

// readWritten is the bytes and info of a file an approach wrote to layer.
func readWritten(layer afero.Fs, path string) ([]byte, os.FileInfo, error) {
	bytes, err := afero.ReadFile(layer, path)
	if err != nil {
		return nil, nil, err
	}
	info, err := layer.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	return bytes, info, nil
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
