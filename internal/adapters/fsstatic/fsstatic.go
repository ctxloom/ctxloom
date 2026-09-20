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
	"path/filepath"
	"sort"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// Static is the writer over one filesystem.
type Static struct{ fs afero.Fs }

var _ delivery.Static = (*Static)(nil)

// New is the static writer over fs.
func New(fs afero.Fs) *Static { return &Static{fs: fs} }

// Deliver validates the target, refuses a plan whose items cannot root under
// it, reverses the writer's previous delivery, then delivers each static
// item and records what it wrote.
func (s *Static) Deliver(ctx context.Context, lo delivery.Loadout, surfaces engine.Surfaces, target delivery.Target) (delivery.Delivered, error) {
	if err := target.Validate(); err != nil {
		return delivery.Delivered{}, err
	}
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
	inputs, err := delivery.InputsFor(lo)
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
			entry := relativeTo(paths, path)
			if _, err := target.Ownership.Apply(ctx, s.fs, path, target.Writer, func([]byte) ([]byte, []string, error) {
				return bytes, []string{entry}, nil
			}); err != nil {
				return delivery.Delivered{}, fmt.Errorf("fsstatic: record %s for %s: %w", path, target.Writer, err)
			}
		}
		out.Presented = append(out.Presented, d.Presented)
		out.Wrote = append(out.Wrote, it.Kind)
	}
	return out, nil
}

// reconcileToEmpty reverses the writer's contribution to every file its
// record names.
func (s *Static) reconcileToEmpty(ctx context.Context, target delivery.Target) error {
	targets, err := target.Ownership.Targets(target.Writer)
	if err != nil {
		return err
	}
	for _, path := range targets {
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

// relativeTo names a written file as the record's entry: its path relative
// to the target root it lies under, or the path itself when it lies under
// none.
func relativeTo(paths present.Paths, path string) string {
	for _, root := range []string{paths.Scratch.Host, paths.EngineHome.Host, paths.ProjectRoot.Host} {
		if root == "" || !present.Under(path, root) {
			continue
		}
		if rel, err := filepath.Rel(root, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return path
}
