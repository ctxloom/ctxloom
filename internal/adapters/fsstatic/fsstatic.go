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
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Static is the writer over one filesystem.
type Static struct{ fs afero.Fs }

var _ delivery.Static = (*Static)(nil)

// New is the static writer over fs.
func New(fs afero.Fs) *Static { return &Static{fs: fs} }

// Deliver validates the target, refuses a plan whose items cannot root under
// it, reverses the writer's previous delivery, then delivers each static
// item and records what it wrote.
func (s *Static) Deliver(ctx context.Context, lo delivery.Loadout, root engine.Base, target delivery.Target) (delivery.Delivered, error) {
	if err := target.Validate(); err != nil {
		return delivery.Delivered{}, err
	}
	surfaces := root.Surfaces()
	paths := target.Root.Paths()
	for _, it := range lo.Plan.Static {
		if surfaces[it.Kind] == nil {
			return delivery.Delivered{}, fmt.Errorf("fsstatic: the plan routes kind %v to approach %q but the engine declares no approach for it", it.Kind, it.Approach)
		}
		if !delivery.HasRoot(paths, it.Root) {
			return delivery.Delivered{}, delivery.Unrootable{Kind: it.Kind, Approach: it.Approach, Needs: it.Root,
				Remedy: fmt.Sprintf("the plan roots kind %v under %v and this target has no such root; select a root the target provides on the binding, or deliver to a target that has it", it.Kind, it.Root)}
		}
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
		layer := afero.NewMemMapFs()
		overlay := afero.NewCopyOnWriteFs(s.fs, layer)
		d, err := deliverKind(surfaces[it.Kind], it.Kind, target.Root, it.Root, inputs, overlay)
		if err != nil {
			return delivery.Delivered{}, fmt.Errorf("fsstatic: deliver %v through %s: %w", it.Kind, it.Approach, err)
		}
		for _, path := range writtenFiles(layer) {
			bytes, err := afero.ReadFile(layer, path)
			if err != nil {
				return delivery.Delivered{}, err
			}
			info, err := layer.Stat(path)
			if err != nil {
				return delivery.Delivered{}, err
			}
			if !underARoot(paths, path) {
				// An approach's OWN state outside the target (claude's MCP
				// approach keeps a hew record under the home): written
				// through as the approach wrote it, never a delivered file
				// the record owns.
				if err := writeThrough(s.fs, path, bytes, info.Mode().Perm()); err != nil {
					return delivery.Delivered{}, fmt.Errorf("fsstatic: write %s: %w", path, err)
				}
				continue
			}
			entry := relativeTo(paths, path)
			if _, err := target.Ownership.Apply(ctx, s.fs, path, target.Writer, func([]byte) ([]byte, []string, error) {
				return bytes, []string{entry}, nil
			}); err != nil {
				return delivery.Delivered{}, fmt.Errorf("fsstatic: record %s for %s: %w", path, target.Writer, err)
			}
			// The record writes the bytes; the mode is the approach's (an
			// exec bit on a skill's script is load-bearing).
			if err := s.fs.Chmod(path, info.Mode().Perm()); err != nil {
				return delivery.Delivered{}, fmt.Errorf("fsstatic: mode of %s: %w", path, err)
			}
		}
		out.Presented = append(out.Presented, d.Presented)
		out.Wrote = append(out.Wrote, it.Kind)
	}
	return out, nil
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
	switch kind {
	case present.Context:
		if c, ok := a.(engine.ContextApproach); ok {
			return c.DeliverContext(start, root, in.Context, fs)
		}
	case present.MCP:
		if c, ok := a.(engine.MCPApproach); ok {
			return c.DeliverMCP(start, root, in.MCP, fs)
		}
	case present.Settings:
		if c, ok := a.(engine.SettingsApproach); ok {
			return c.DeliverSettings(start, root, in.Settings, fs)
		}
	case present.Hooks:
		if c, ok := a.(engine.HooksApproach); ok {
			return c.DeliverHooks(start, root, in.Hooks, fs)
		}
	case present.Commands:
		if c, ok := a.(engine.CommandsApproach); ok {
			return c.DeliverCommands(start, root, in.Commands, fs)
		}
	case present.Skills:
		if c, ok := a.(engine.SkillsApproach); ok {
			return c.DeliverSkills(start, root, in.Skills, fs)
		}
	}
	return present.Delivered{}, fmt.Errorf("approach %q does not deliver kind %v", a.Name(), kind)
}

// writtenFiles lists every regular file the approach left in the overlay's
// layer, sorted.
func writtenFiles(layer afero.Fs) []string {
	var out []string
	_ = afero.Walk(layer, string(filepath.Separator), func(p string, info fs.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}

// writeThrough lands a file the approach wrote outside the target's roots
// on the real filesystem, bytes and mode as written.
func writeThrough(fs afero.Fs, path string, bytes []byte, mode os.FileMode) error {
	if err := fs.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return iox.WriteFileAtomicFs(fs, path, bytes, mode)
}

// rootsOf are the target's resolved roots, in the order a written file is
// attributed to them.
func rootsOf(paths present.Paths) []string {
	return []string{paths.Scratch.Host, paths.EngineHome.Host, paths.ProjectRoot.Host}
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
