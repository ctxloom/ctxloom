package operations

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// Placement is where and under whom one engine's decoded package lands: the
// placement core's parameters, so neither the root nor the writer family is
// hard-coded in it. An at-rest caller fixes Start to
// present.ProjectOnHost(target), Family to delivery.ProjectWriterFor(engine),
// Kinds to the kinds it speaks for and Roots to nil. A session-home caller
// would pass the cell, delivery.SessionWriter(harp), nil Kinds and the
// binding's roots.
type Placement struct {
	Start  present.Start                     // the roots
	Family delivery.Writer                   // the writer family base
	Kinds  []present.Kind                    // nil = one writer for every kind
	Roots  map[present.Kind]present.RootKind // nil = each approach's first offered root
}

// atRestPlacement is the at-rest placement into dir for one engine: the
// project root, the engine's project writer family, the given kinds.
func atRestPlacement(dir string, name engine.Name, kinds []present.Kind) Placement {
	return Placement{Start: present.ProjectOnHost(dir), Family: delivery.ProjectWriterFor(name), Kinds: kinds}
}

// servesEndpoint reports whether a placement is a live session's: only a
// session serves ctxloom's MCP endpoint, so only its plan carries it.
func (p Placement) servesEndpoint() bool {
	_, ok := p.Family.SessionHarp()
	return ok
}

// Deliver is the placement core: the engine's Exports, delivery.PlanFor and
// delivery.TargetFor at p, and one Static.Deliver. It assembles nothing and
// reads no config. extra carries what only a live session has (its MCP
// endpoint, Identity, Index, SessionHome) and is zero at rest; its Plan,
// Package and Exports are overwritten.
func Deliver(ctx context.Context, fsRoot safefs.Root, kind engine.Engine, pkg composite.Package, extra delivery.Loadout, p Placement) (delivery.Delivered, delivery.Plan, error) {
	root := kind.Root()
	items := pkg.EngineItems(root.Name)
	exports, err := kind.Exports(items)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, fmt.Errorf("%s exports: %w", root.Name, err)
	}
	paths := p.Start.Paths()
	plan, err := delivery.PlanFor(root, items, paths, p.Roots, p.Kinds, p.servesEndpoint())
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, err
	}
	records, err := OwnershipRecordsOn(fsRoot.Fs)
	if err != nil {
		return delivery.Delivered{}, delivery.Plan{}, err
	}
	lo := extra
	lo.Plan, lo.Package, lo.Exports = plan, pkg, exports
	if lo.WorkDir == "" {
		lo.WorkDir = paths.ProjectRoot.Host
	}
	d, err := fsstatic.New(fsRoot).Deliver(ctx, lo, root, delivery.TargetFor(p.Start, records, p.Family, p.Kinds))
	return d, plan, err
}

// Release delivers the EMPTY plan at p: the record says what p's writers
// (one per kind when p.Kinds is set) put there, and only that leaves.
func Release(ctx context.Context, fsRoot safefs.Root, kind engine.Engine, p Placement) error {
	records, err := OwnershipRecordsOn(fsRoot.Fs)
	if err != nil {
		return err
	}
	lo := delivery.Loadout{WorkDir: p.Start.Paths().ProjectRoot.Host}
	_, err = fsstatic.New(fsRoot).Deliver(ctx, lo, kind.Root(), delivery.TargetFor(p.Start, records, p.Family, p.Kinds))
	return err
}
